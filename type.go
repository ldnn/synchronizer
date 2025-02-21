package main

import (
	"github.com/segmentio/kafka-go"
)

type Synchronizer struct {
	Token       string
	KafkaWriter *kafka.Writer
	Host        string
}

type TokenResponse struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	RefreshToken string `json:"refresh_token"`
}

type Arg struct {
	User       string
	Passwd     string
	Host       string
	KafkaAddr  string
	KafkaTopic string
}
