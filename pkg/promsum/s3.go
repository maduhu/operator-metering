package promsum

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"path/filepath"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3iface"
	"github.com/segmentio/ksuid"

	cb "github.com/coreos-inc/kube-chargeback/pkg/chargeback/v1"
)

// NewS3Store configures an S3 client and returns a Store for a given bucket and path.
func NewS3Store(bucket, path string) S3Store {
	awsSession := session.Must(session.NewSession())
	return S3Store{
		Bucket: bucket,
		Path:   path,
		s3:     s3.New(awsSession),
	}
}

// S3Store is a implementation of an S3 backed Store.
type S3Store struct {
	Bucket string
	Path   string
	s3     s3iface.S3API
}

// S3Store must implement the Store interface
var _ Store = S3Store{}

// Write stores a billing record in an S3 bucket at under the given path.
// Will overwrite existing entries matching range, subject, and query.
func (s S3Store) Write(records []BillingRecord) error {
	uuid, err := ksuid.NewRandom()
	if err != nil {
		return fmt.Errorf("failed to generate file uuid: %s", err)
	}

	min, max := extrema(records)
	rng := cb.Range{min, max}
	name := Name(rng, uuid.String())
	key := filepath.Join(s.Path, name)

	data, err := encodeRecords(records, name)
	if err != nil {
		return fmt.Errorf("could not encode record: %v", err)
	}

	_, err = s.s3.PutObject(&s3.PutObjectInput{
		Bucket: aws.String(s.Bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(data),
	})

	return err
}

// Read retrieves all billing records for the given range, query, and subject. There are no ordering guarantees.
func (s S3Store) Read(rng cb.Range, query, subject string) (records []BillingRecord, err error) {
	list, err := s.s3.ListObjectsV2(&s3.ListObjectsV2Input{
		Bucket: aws.String(s.Bucket),
		Prefix: aws.String(s.Path),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list S3 for 's3://%s/%s': %v", s.Bucket, s.Path, err)
	}

	for _, obj := range list.Contents {
		if ok, err := PathWithinRange(*obj.Key, rng); err != nil {
			return nil, fmt.Errorf("failed to determine if path '%s' is in range: %v", *obj.Key, err)
		} else if !ok {
			continue
		}

		out, err := s.s3.GetObject(&s3.GetObjectInput{
			Bucket: aws.String(s.Bucket),
			Key:    aws.String(*obj.Key),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to retrieve 's3://%s/%s': %v", s.Bucket, *obj.Key, err)
		}

		data, err := ioutil.ReadAll(out.Body)
		if err != nil {
			return nil, fmt.Errorf("failed to read body from S3 response for 's3://%s/%s': %v",
				s.Bucket, obj.Key, err)
		}

		fileRecords, err := decodeRelevantRecords(data, rng)
		if err != nil {
			return nil, fmt.Errorf("failed to read file '%s': %v", *obj.Key, err)
		}
		records = append(records, fileRecords...)
	}
	return
}
