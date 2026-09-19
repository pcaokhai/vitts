package s3

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/google/uuid"
)

// JobTexts stores a job's input text and signs its output link.
//
// Text lives here and never in Postgres (ADR-008), under a prefix a lifecycle rule
// expires after two days (docs/05-data-model.md § 3).
type JobTexts struct {
	client *Client
}

// NewJobTexts wires the adapter.
func NewJobTexts(client *Client) *JobTexts { return &JobTexts{client: client} }

// PutText writes the job's input.
func (j *JobTexts) PutText(ctx context.Context, tenantID, jobID uuid.UUID, text string) error {
	key := InputKey(tenantID, jobID)
	if err := j.client.Put(ctx, key, bytes.NewReader([]byte(text)), int64(len(text))); err != nil {
		return fmt.Errorf("put job text: %w", err)
	}
	return nil
}

// GetText reads the job's input back for segmentation.
func (j *JobTexts) GetText(ctx context.Context, tenantID, jobID uuid.UUID) (string, error) {
	body, err := j.client.Get(ctx, InputKey(tenantID, jobID))
	if err != nil {
		return "", fmt.Errorf("get job text: %w", err)
	}
	defer func() { _ = body.Close() }()

	raw, err := io.ReadAll(body)
	if err != nil {
		return "", fmt.Errorf("read job text: %w", err)
	}
	return string(raw), nil
}

// DeleteText removes the input once the job reaches a terminal state.
func (j *JobTexts) DeleteText(ctx context.Context, tenantID, jobID uuid.UUID) error {
	if err := j.client.Delete(ctx, InputKey(tenantID, jobID)); err != nil {
		return fmt.Errorf("delete job text: %w", err)
	}
	return nil
}

// SignedOutputURL returns a time-limited download link.
//
// The object is never made public: a signed URL is scoped to one object and expires,
// which is what US-14 acceptance criterion 4 asks for.
func (j *JobTexts) SignedOutputURL(ctx context.Context, key string, ttl time.Duration) (string, error) {
	signer := awss3.NewPresignClient(j.client.api)

	request, err := signer.PresignGetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(j.client.bucket),
		Key:    aws.String(key),
	}, awss3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("presign %s: %w", key, err)
	}
	return request.URL, nil
}

// Object layout from docs/05-data-model.md § 3.
const (
	inputSuffix  = "input.txt"
	outputPrefix = "jobs"
)

// InputKey is where a job's text lives.
func InputKey(tenantID, jobID uuid.UUID) string {
	return fmt.Sprintf("%s/%s/%s/%s", outputPrefix, tenantID, jobID, inputSuffix)
}

// SegmentKey is where one synthesized segment lives.
func SegmentKey(tenantID, jobID uuid.UUID, seq int32) string {
	return fmt.Sprintf("%s/%s/%s/seg/%05d.pcm", outputPrefix, tenantID, jobID, seq)
}

// OutputKey is where the merged file lives.
func OutputKey(tenantID, jobID uuid.UUID, format string) string {
	ext := map[string]string{"mp3": "mp3", "ogg_opus": "ogg", "wav": "wav"}[format]
	if ext == "" {
		ext = "bin"
	}
	return fmt.Sprintf("%s/%s/%s/output.%s", outputPrefix, tenantID, jobID, ext)
}
