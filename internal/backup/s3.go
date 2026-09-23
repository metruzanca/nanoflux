package backup

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Destination stores archives as objects in an S3-compatible bucket, under an
// optional key prefix.
type S3Destination struct {
	client *minio.Client
	bucket string
	prefix string // "" or "foo/bar" (no leading/trailing slash)
}

// S3Options configures an S3Destination. Endpoint may carry an http(s)://
// scheme; https enables TLS. An empty prefix stores objects at the bucket root.
type S3Options struct {
	Endpoint  string
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string
	Prefix    string
}

// NewS3 builds an S3Destination and verifies the bucket is reachable.
func NewS3(ctx context.Context, o S3Options) (*S3Destination, error) {
	endpoint := strings.TrimPrefix(strings.TrimPrefix(o.Endpoint, "https://"), "http://")
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(o.AccessKey, o.SecretKey, ""),
		Secure: strings.HasPrefix(o.Endpoint, "https://"),
		Region: o.Region,
	})
	if err != nil {
		return nil, fmt.Errorf("backup: s3 client: %w", err)
	}
	exists, err := client.BucketExists(ctx, o.Bucket)
	if err != nil {
		return nil, fmt.Errorf("backup: reach bucket %q: %w", o.Bucket, err)
	}
	if !exists {
		return nil, fmt.Errorf("backup: bucket %q does not exist (create it first)", o.Bucket)
	}
	return &S3Destination{
		client: client,
		bucket: o.Bucket,
		prefix: strings.Trim(o.Prefix, "/"),
	}, nil
}

func (d *S3Destination) key(name string) string {
	if d.prefix == "" {
		return name
	}
	return d.prefix + "/" + name
}

func (d *S3Destination) Put(ctx context.Context, name string, r io.Reader) error {
	// minio needs a known object size for a plain single-part put; snapshots
	// are small (a few MB), so buffer to learn the length. This also makes the
	// upload a single atomic request rather than a multipart stream.
	data, err := io.ReadAll(r)
	if err != nil {
		return fmt.Errorf("backup: read archive: %w", err)
	}
	if _, err := d.client.PutObject(ctx, d.bucket, d.key(name), bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{ContentType: "application/gzip"}); err != nil {
		return fmt.Errorf("backup: s3 put %s: %w", name, err)
	}
	return nil
}

func (d *S3Destination) List(ctx context.Context) ([]Snapshot, error) {
	var out []Snapshot
	// List under the prefix directory only, so objects that merely share a name
	// prefix with another "folder" are not picked up.
	listPrefix := d.prefix
	if listPrefix != "" {
		listPrefix += "/"
	}
	for obj := range d.client.ListObjects(ctx, d.bucket, minio.ListObjectsOptions{Prefix: listPrefix, Recursive: true}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("backup: s3 list: %w", obj.Err)
		}
		name := strings.TrimPrefix(obj.Key, listPrefix)
		if !strings.HasSuffix(name, ".tar.gz") {
			continue
		}
		out = append(out, Snapshot{Name: name, Size: obj.Size, Modified: obj.LastModified})
	}
	return out, nil
}

func (d *S3Destination) Delete(ctx context.Context, name string) error {
	if err := d.client.RemoveObject(ctx, d.bucket, d.key(name), minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("backup: s3 delete %s: %w", name, err)
	}
	return nil
}
