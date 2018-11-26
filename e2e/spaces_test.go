package e2e

import (
	"fmt"
	"os"

	minio "github.com/minio/minio-go"
)

type s3Client struct {
	*minio.Client
}

func createS3Client() (*s3Client, error) {
	cl, err := minio.New(os.Getenv("S3_ENDPOINT"), os.Getenv("S3_ACCESS_KEY_ID"), os.Getenv("S3_SECRET_ACCESS_KEY"), true)
	if err != nil {
		return nil, fmt.Errorf("failed to create S3 client: %s", err)
	}

	return &s3Client{
		Client: cl,
	}, nil
}

func (cl *s3Client) ensureSpace(name string) error {
	found, err := cl.BucketExists(name)
	if err != nil {
		return fmt.Errorf("failed to check for existance of bucket %q: %s", name, err)
	}

	if !found {
		if err := cl.MakeBucket(name, "us-east-1"); err != nil {
			return fmt.Errorf("failed to create bucket %q: %s", name, err)
		}
	} else {
		fmt.Printf("Space %q exists already\n", name)
	}

	return nil
}

func (cl *s3Client) deleteSpace(name string) error {
	found, err := cl.BucketExists(name)
	if err != nil {
		return fmt.Errorf("failed to check for existance of bucket %q: %s", name, err)
	}

	if found {
		// Delete all bucket objects.
		listCh := make(chan string)
		errCh := make(chan error)

		go func() {
			defer close(listCh)
			for object := range cl.ListObjects(name, "", true, nil) {
				if object.Err != nil {
					errCh <- object.Err
					return
				}
				listCh <- object.Key
			}
		}()

		remCh := cl.RemoveObjects(name, listCh)
		select {
		case err := <-errCh:
			return fmt.Errorf("failed to list objects: %s", err)
		case err, ok := <-remCh:
			if ok {
				return fmt.Errorf("failed to delete all objects: %s", err)
			}
		}

		if err := cl.RemoveBucket(name); err != nil {
			return fmt.Errorf("failed to remove bucket %q: %s", name, err)
		}
	}

	return nil
}
