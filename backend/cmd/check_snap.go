package main

import (
	"context"
	"fmt"
	"os"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

func main() {
	c, err := minio.New("localhost:9000", &minio.Options{
		Creds:  credentials.NewStaticV4("nvr", "nvr_secret", ""),
		Secure: false,
	})
	if err != nil {
		fmt.Println("клиент:", err)
		os.Exit(1)
	}
	ctx := context.Background()
	key := "snapshots/ab1b906d-a232-4568-9e9d-0eb1da455f97/2026-09-23/112920.003_person.jpg"

	// Пробуем StatObject
	info, err := c.StatObject(ctx, "nvr-recordings", key, minio.StatObjectOptions{})
	if err != nil {
		fmt.Printf("StatObject ОШИБКА: %v\n", err)
	} else {
		fmt.Printf("StatObject OK: размер=%d тип=%s\n", info.Size, info.ContentType)
	}

	// Пробуем GetObject
	obj, err := c.GetObject(ctx, "nvr-recordings", key, minio.GetObjectOptions{})
	if err != nil {
		fmt.Printf("GetObject ОШИБКА: %v\n", err)
		os.Exit(1)
	}
	st, err := obj.Stat()
	if err != nil {
		fmt.Printf("obj.Stat ОШИБКА: %v\n", err)
	} else {
		fmt.Printf("obj.Stat OK: размер=%d\n", st.Size)
	}
}
