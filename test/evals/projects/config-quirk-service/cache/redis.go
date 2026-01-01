package cache

// RedisConfig holds Redis connection settings.
type RedisConfig struct {
	Host string
	Port int
}

// RedisClient represents a Redis connection.
type RedisClient struct {
	config RedisConfig
}

// NewRedisClient creates a new Redis client.
// TODO: Set up connection with correct configuration.
func NewRedisClient() *RedisClient {
	return &RedisClient{
		config: RedisConfig{
			Host: "localhost",
			// Port needs to be set correctly
		},
	}
}

// GetConfig returns the client's configuration.
func (c *RedisClient) GetConfig() RedisConfig {
	return c.config
}
