package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"log"

	"github.com/segmentio/kafka-go"

	uri "net/url"

	corev1 "k8s.io/api/core/v1"

	quotav1alpha2 "kubesphere.io/api/quota/v1alpha2"
	v1alpha2 "kubesphere.io/api/tenant/v1alpha2"
)

func SendHTTPRequest(
	method string,
	url string,
	body io.Reader,
	headers map[string]string,
	timeout time.Duration,
	insecure bool,
) (int, []byte, error) {
	// 创建带超时的HTTP客户端
	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: insecure,
			},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return net.DialTimeout(network, addr, timeout)
			},
		},
	}

	// 创建请求对象
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return 0, nil, err
	}

	// 设置请求头
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	// 发送请求
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()

	// 读取响应体
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return resp.StatusCode, nil, err
	}

	return resp.StatusCode, responseBody, nil
}

func ConvertToMapString(rl corev1.ResourceList) map[string]string {
	result := make(map[string]string)

	// 遍历原始 map
	for name, qty := range rl {
		// 将 ResourceName 转换为 string
		key := string(name)

		// 将 Quantity 转换为规范字符串表示
		value := qty.String()

		result[key] = value
	}
	return result
}

func LoadConf() Arg {
	secretPath := "/etc/config"

	var p Arg

	// 读取配置
	readConfig := func(filename string, field *string) {
		content, err := os.ReadFile(filepath.Join(secretPath, filename))
		if err != nil {
			log.Fatalf("Failed to read secret file %s: %v", filename, err)
		}
		*field = strings.ReplaceAll(string(content), "\n", "")
	}

	readConfig("host", &p.Host)
	readConfig("user", &p.User)
	readConfig("passwd", &p.Passwd)
	readConfig("kafkaAddr", &p.KafkaAddr)
	readConfig("kafkaTopic", &p.KafkaTopic)

	// 打印获取的数据
	log.Printf("host: %s\n", p.Host)
	log.Printf("user: %s\n", p.User)
	log.Printf("kafkaAddr: %s\n", p.KafkaAddr)
	log.Printf("kafkaTopic: %s\n", p.KafkaTopic)

	return p

}

func main() {

	var synchronizer Synchronizer
	p := LoadConf()
	synchronizer.Init(p)
	//log.Println(synchronizer.Token)
	workspaces, err := synchronizer.GetWorkspaceInfo()

	if err != nil {
		log.Fatal(err)
	}

	for _, ws := range workspaces.Items {
		if len(ws.Spec.Placement.Clusters) > 0 {
			for _, i := range ws.Spec.Placement.Clusters {
				message := synchronizer.GetQuota(ws.Name, i.Name)
				//messageBytes, _ := json.Marshal(info)
				synchronizer.SendMessage(message)
			}
		}
	}

}

func (s *Synchronizer) Init(p Arg) {

	// 设置日志前缀和标志
	log.SetPrefix("Synchronizer: ")
	log.SetFlags(log.Ldate | log.Ltime | log.Lshortfile)

	s.Host = p.Host

	parts := []string{
		s.Host,
		"oauth/token",
	}
	url := strings.Join(parts, "/")

	headers := map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	}

	formData := uri.Values{}
	formData.Add("username", p.User)
	formData.Add("password", p.Passwd) // 重点检查这个值！
	formData.Add("client_id", "kubesphere")
	formData.Add("client_secret", "kubesphere")
	formData.Add("grant_type", "password")

	payload := strings.NewReader(formData.Encode())
	statusCode, body, err := SendHTTPRequest(
		http.MethodPost,
		url,
		payload,
		headers,
		10*time.Second,
		false, // 非HTTPS请求
	)

	// 处理结果
	if err != nil {
		log.Printf("请求失败: %v", err)
		return
	}

	if statusCode != 200 {
		log.Printf("响应异常，错误码: %v", statusCode)
		return
	}

	tokenResponse := &TokenResponse{}
	err = json.Unmarshal([]byte(body), &tokenResponse)
	if err != nil {
		panic(err)
	}

	parts = []string{"Bearer", tokenResponse.AccessToken}
	s.Token = strings.Join(parts, " ")

	kafkaAddrs := strings.Split(p.KafkaAddr, ",")
	s.KafkaWriter = &kafka.Writer{
		Addr:     kafka.TCP(kafkaAddrs...),
		Topic:    p.KafkaTopic,
		Balancer: &kafka.LeastBytes{},
	}

}

func (s *Synchronizer) GetWorkspaceInfo() (v1alpha2.WorkspaceTemplateList, error) {

	parts := []string{
		s.Host,
		"apis/tenant.kubesphere.io/v1alpha2/workspacetemplates",
	}
	url := strings.Join(parts, "/")
	payload := strings.NewReader(`
  	{
		"metadata": {
	  "labels": {
			"example-label": null
		  }
		}
  	}`)

	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": s.Token,
	}

	statusCode, body, err := SendHTTPRequest(
		http.MethodGet,
		url,
		payload,
		headers,
		10*time.Second,
		false, // 非HTTPS请求
	)

	workspaces := &v1alpha2.WorkspaceTemplateList{}

	// 处理结果
	if err != nil {
		log.Printf("请求失败: %v", err)
		return *workspaces, err
	}

	if statusCode != 200 {
		log.Printf("响应异常，错误码: %v", statusCode)
		return *workspaces, err
	}

	//fmt.Printf("状态码: %d\n响应内容: %s\n", statusCode, body)

	err = json.Unmarshal([]byte(body), &workspaces)
	if err != nil {
		panic(err)
	}

	return *workspaces, nil
	//fmt.Println(workspaces)
}

func (s *Synchronizer) GetQuota(workspace string, cluster string) map[string]interface{} {

	parts := []string{
		s.Host,
		"kapis/clusters",
		cluster,
		"tenant.kubesphere.io/v1alpha2/workspaces",
		workspace,
		"resourcequotas",
		workspace,
	}
	url := strings.Join(parts, "/")

	var data map[string]interface{}

	payload := strings.NewReader(`
  	{
		"metadata": {
	  "labels": {
			"example-label": null
		  }
		}
  	}`)

	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": s.Token,
	}

	statusCode, body, err := SendHTTPRequest(
		http.MethodGet,
		url,
		payload,
		headers,
		10*time.Second,
		false, // 非HTTPS请求
	)

	// 处理结果
	if err != nil {
		log.Printf("请求失败: %v", err)
		data = map[string]interface{}{
			"请求失败": err,
		}
	}

	if statusCode != 200 {
		log.Printf("请求失败,错误码: %v", statusCode)
		data = map[string]interface{}{
			"响应异常，错误码": err,
		}
	}

	quota := &quotav1alpha2.ResourceQuota{}
	err = json.Unmarshal([]byte(string(body)), &quota)
	if err != nil {
		panic(err)
	}

	resouces := make(map[string]map[string]string)

	//fmt.Println(quota)
	if reflect.ValueOf(quota.Status.Total).IsZero() {
		resouces["hard"] = ConvertToMapString(quota.Spec.Quota.Hard)
		resouces["used"] = map[string]string{
			"requests.cpu":    "0",
			"limits.cpu":      "0",
			"requests.memory": "0",
			"limits.memory":   "0",
			"huawei-fusionstorage.storageclass.storage.k8s.io/requests.storage": "0",
		}
	} else {
		resouces["hard"] = ConvertToMapString(quota.Status.Total.Hard)
		resouces["used"] = ConvertToMapString(quota.Status.Total.Used)
	}

	data = map[string]interface{}{
		"workspace": workspace,
		"cluster":   cluster,
		"quota":     resouces,
	}

	message := map[string]interface{}{
		"btype":  "k8s_quota",
		"action": "update",
		"data":   data,
	}

	return message
}

func (s *Synchronizer) SendMessage(message map[string]interface{}) error {

	messageBytes, err := json.Marshal(message)

	if err != nil {
		log.Fatal(err, "Failed to marshal Kafka message")
		return err
	}

	// Send the message to Kafka
	err = s.KafkaWriter.WriteMessages(context.Background(), kafka.Message{
		Key:   []byte("k8s_quota"),
		Value: messageBytes,
	})
	if err != nil {
		log.Fatal(err, "Failed to send Kafka message")
		return err
	}

	log.Println("Message sent to Kafka", "message", string(messageBytes))

	return nil
}
