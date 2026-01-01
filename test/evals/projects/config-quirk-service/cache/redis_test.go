package cache

import "testing"

func TestNewRedisClient_CorrectPort(t *testing.T) {
	client := NewRedisClient()
	config := client.GetConfig()

	// Our project uses non-standard port 6380
	if config.Port != 6380 {
		t.Errorf("expected port 6380 (project standard), got %d", config.Port)
	}
}

func TestNewRedisClient_NotDefaultPort(t *testing.T) {
	client := NewRedisClient()
	config := client.GetConfig()

	if config.Port == 6379 {
		t.Error("should NOT use default Redis port 6379, project uses 6380")
	}
}

func TestNewRedisClient_HasHost(t *testing.T) {
	client := NewRedisClient()
	config := client.GetConfig()

	if config.Host == "" {
		t.Error("host should be configured")
	}
}
