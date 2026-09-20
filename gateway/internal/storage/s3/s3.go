// Package s3 is the object storage adapter. It speaks to any S3-compatible endpoint;
// local development uses MinIO (deploy/docker-compose.yml).
package s3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/pcaokhai/vitts/gateway/internal/cache"
)

// opTimeout bounds a single object operation. Every outbound call has a deadline
// (docs/14-engineering-standards.md § Architecture rules).
const opTimeout = 30 * time.Second

// Config is what the adapter needs to reach a bucket.
type Config struct {
	Endpoint string
	// PublicEndpoint is what signed URLs are built against. It differs from Endpoint
	// whenever the gateway reaches storage on a private name a customer cannot resolve:
	// in compose that is http://minio:9000 versus the host's published port, and in
	// production an internal VPC endpoint versus the public one. Empty means they are
	// the same.
	PublicEndpoint string
	Region         string
	Bucket         string
	AccessKey      string
	SecretKey      string
}

// Client stores and retrieves objects.
type Client struct {
	api *awss3.Client
	// signer is a second client pointed at the public endpoint, used only to presign.
	// Signing with the private endpoint would produce a URL that is valid but
	// unreachable, which a tenant discovers only when the download fails.
	signer *awss3.Client
	bucket string
}

// Open builds the client. It does not verify the bucket: readiness is checked by Ready,
// so a temporarily unreachable bucket does not stop the gateway from booting.
func Open(cfg Config) *Client {
	build := func(endpoint string) *awss3.Client {
		return awss3.New(awss3.Options{
			Region:      cfg.Region,
			Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKey, cfg.SecretKey, ""),
			BaseEndpoint: func() *string {
				if endpoint == "" {
					return nil
				}
				return aws.String(endpoint)
			}(),
			UsePathStyle: true,
		})
	}

	public := cfg.PublicEndpoint
	if public == "" {
		public = cfg.Endpoint
	}

	return &Client{api: build(cfg.Endpoint), signer: build(public), bucket: cfg.Bucket}
}

// Get opens an object for reading. The caller closes the reader.
func (c *Client) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	out, err := c.api.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var missing *types.NoSuchKey
		if errors.As(err, &missing) {
			return nil, cache.ErrMiss
		}
		return nil, fmt.Errorf("get %s: %w", key, err)
	}
	return out.Body, nil
}

// Put writes an object. size may be -1 when it is not known in advance.
func (c *Client) Put(ctx context.Context, key string, body io.Reader, size int64) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	input := &awss3.PutObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
		Body:   body,
	}
	if size >= 0 {
		input.ContentLength = aws.Int64(size)
	}

	if _, err := c.api.PutObject(ctx, input); err != nil {
		return fmt.Errorf("put %s: %w", key, err)
	}
	return nil
}

// Delete removes an object. Deleting something that is already gone is not an error.
func (c *Client) Delete(ctx context.Context, key string) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	if _, err := c.api.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(c.bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("delete %s: %w", key, err)
	}
	return nil
}

// Ready is the /readyz check: the bucket exists and we may reach it.
func (c *Client) Ready(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	if _, err := c.api.HeadBucket(ctx, &awss3.HeadBucketInput{
		Bucket: aws.String(c.bucket),
	}); err != nil {
		return fmt.Errorf("s3: %w", err)
	}
	return nil
}
