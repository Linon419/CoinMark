package redis

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

type Store struct {
	Client *redis.Client
}

func Open(url string) (*Store, error) {
	opts, err := redis.ParseURL(url)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, err
	}
	return &Store{Client: client}, nil
}

func (s *Store) Close() error {
	return s.Client.Close()
}

func (s *Store) Get(ctx context.Context, key string) (string, error) {
	return s.Client.Get(ctx, key).Result()
}

func (s *Store) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	return s.Client.Set(ctx, key, value, ttl).Err()
}

func (s *Store) Del(ctx context.Context, keys ...string) error {
	return s.Client.Del(ctx, keys...).Err()
}
