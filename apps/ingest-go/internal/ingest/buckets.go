package ingest

import (
	"fmt"
	"time"
)

func BucketMS(bucket string) (int64, error) {
	switch bucket {
	case "1m":
		return 60 * 1000, nil
	case "15m":
		return 15 * 60 * 1000, nil
	case "1h":
		return 60 * 60 * 1000, nil
	case "4h":
		return 4 * 60 * 60 * 1000, nil
	case "1d":
		return 24 * 60 * 60 * 1000, nil
	default:
		return 0, fmt.Errorf("unsupported bucket: %s", bucket)
	}
}

func FloorBucketStartMS(tsMS int64, bucket string) (int64, error) {
	size, err := BucketMS(bucket)
	if err != nil {
		return 0, err
	}
	return (tsMS / size) * size, nil
}

func UtcNowMS() int64 {
	return time.Now().UTC().UnixMilli()
}
