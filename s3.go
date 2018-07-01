package main

import (
	"os"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go/service/s3"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
)

type AWSCredential struct {
	AccessKeyID     string
	SecretAccessKey string
	Region          string
	BucketName      string
	BasePath        string
}

func NewAWSCredential(accessKeyID, secretAccessKey, region, bucketName, basePath string) *AWSCredential {
	return &AWSCredential{
		accessKeyID,
		secretAccessKey,
		region,
		bucketName,
		basePath,
	}
}

func getContentType(path string) string {
	normalizedPath := strings.ToLower(path)
	if strings.HasSuffix(normalizedPath, "png") {
		return "image/png"
	} else if strings.HasSuffix(normalizedPath, "jpg") {
		return "image/jpeg"
	} else if strings.HasSuffix(normalizedPath, "jpeg") {
		return "image/jpeg"
	} else if strings.HasSuffix(normalizedPath, "gif") {
		return "image/gif"
	}
	return "image/jpeg"
}

func (cred *AWSCredential) Put(filepath string) error {
	file, err := os.Open(filepath)
	if err != nil {
		return err
	}
	destinationPath := path.Join(cred.BasePath, path.Base(filepath))
	defer file.Close()
	sess, err := session.NewSession()
	if err != nil {
		return err
	}
	s3session := s3.New(sess)
	_, err = s3session.PutObject(&s3.PutObjectInput{
		Bucket:      aws.String(cred.BucketName),
		Key:         aws.String(destinationPath),
		Body:        file,
		ACL:         aws.String("public-read"),
		ContentType: aws.String(getContentType(filepath)),
	})
	if err != nil {
		return err
	}
	return nil
}
