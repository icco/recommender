package lock

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// DistributedLock provides distributed locking functionality using etcd
type DistributedLock struct {
	client *clientv3.Client
	logger *slog.Logger
	
	// For local fallback when etcd is not available
	localMutex sync.Mutex
	localLocks map[string]*sync.Mutex
	useLocal   bool
}

// NewDistributedLock creates a new distributed lock instance
func NewDistributedLock(endpoints []string, logger *slog.Logger) (*DistributedLock, error) {
	dl := &DistributedLock{
		logger:     logger,
		localLocks: make(map[string]*sync.Mutex),
		useLocal:   false,
	}

	// Try to connect to etcd
	if len(endpoints) > 0 {
		client, err := clientv3.New(clientv3.Config{
			Endpoints:   endpoints,
			DialTimeout: 5 * time.Second,
		})
		if err != nil {
			logger.Warn("Failed to connect to etcd, falling back to local locking", 
				slog.Any("error", err))
			dl.useLocal = true
		} else {
			dl.client = client
			logger.Info("Successfully connected to etcd for distributed locking")
		}
	} else {
		logger.Info("No etcd endpoints provided, using local locking")
		dl.useLocal = true
	}

	return dl, nil
}

// Lock acquires a distributed lock with the given key
func (dl *DistributedLock) Lock(ctx context.Context, key string, ttl time.Duration) (*Lock, error) {
	if dl.useLocal {
		return dl.lockLocal(key), nil
	}
	return dl.lockEtcd(ctx, key, ttl)
}

// lockLocal implements local locking as a fallback
func (dl *DistributedLock) lockLocal(key string) *Lock {
	dl.localMutex.Lock()
	defer dl.localMutex.Unlock()
	
	if _, exists := dl.localLocks[key]; !exists {
		dl.localLocks[key] = &sync.Mutex{}
	}
	
	mutex := dl.localLocks[key]
	mutex.Lock()
	
	return &Lock{
		key:        key,
		isLocal:    true,
		localMutex: mutex,
		dl:         dl,
	}
}

// lockEtcd implements distributed locking using etcd
func (dl *DistributedLock) lockEtcd(ctx context.Context, key string, ttl time.Duration) (*Lock, error) {
	// Create a lease
	lease, err := dl.client.Grant(ctx, int64(ttl.Seconds()))
	if err != nil {
		return nil, fmt.Errorf("failed to create lease: %w", err)
	}

	// Try to acquire the lock
	lockKey := fmt.Sprintf("/locks/%s", key)
	txn := dl.client.Txn(ctx).If(
		clientv3.Compare(clientv3.CreateRevision(lockKey), "=", 0),
	).Then(
		clientv3.OpPut(lockKey, "locked", clientv3.WithLease(lease.ID)),
	).Else(
		clientv3.OpGet(lockKey),
	)

	resp, err := txn.Commit()
	if err != nil {
		dl.client.Revoke(ctx, lease.ID)
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}

	if !resp.Succeeded {
		dl.client.Revoke(ctx, lease.ID)
		return nil, fmt.Errorf("lock already held by another process")
	}

	// Keep the lease alive
	ch, kaerr := dl.client.KeepAlive(ctx, lease.ID)
	if kaerr != nil {
		dl.client.Revoke(ctx, lease.ID)
		return nil, fmt.Errorf("failed to keep lease alive: %w", kaerr)
	}

	// Start goroutine to consume keep-alive responses
	go func() {
		for ka := range ch {
			if ka == nil {
				dl.logger.Warn("Keep-alive channel closed for lock", slog.String("key", key))
				return
			}
		}
	}()

	return &Lock{
		key:     key,
		leaseID: lease.ID,
		dl:      dl,
	}, nil
}

// Close closes the distributed lock client
func (dl *DistributedLock) Close() error {
	if dl.client != nil {
		return dl.client.Close()
	}
	return nil
}

// Lock represents an acquired lock
type Lock struct {
	key        string
	leaseID    clientv3.LeaseID
	isLocal    bool
	localMutex *sync.Mutex
	dl         *DistributedLock
}

// Unlock releases the lock
func (l *Lock) Unlock(ctx context.Context) error {
	if l.isLocal {
		l.localMutex.Unlock()
		return nil
	}

	// Revoke the lease to release the lock
	if _, err := l.dl.client.Revoke(ctx, l.leaseID); err != nil {
		return fmt.Errorf("failed to revoke lease: %w", err)
	}

	return nil
}

// TryLock attempts to acquire a lock without blocking
func (dl *DistributedLock) TryLock(ctx context.Context, key string, ttl time.Duration) (*Lock, error) {
	if dl.useLocal {
		return dl.tryLockLocal(key), nil
	}
	return dl.lockEtcd(ctx, key, ttl)
}

// tryLockLocal attempts to acquire a local lock without blocking
func (dl *DistributedLock) tryLockLocal(key string) *Lock {
	dl.localMutex.Lock()
	defer dl.localMutex.Unlock()
	
	if _, exists := dl.localLocks[key]; !exists {
		dl.localLocks[key] = &sync.Mutex{}
	}
	
	mutex := dl.localLocks[key]
	if mutex.TryLock() {
		return &Lock{
			key:        key,
			isLocal:    true,
			localMutex: mutex,
			dl:         dl,
		}
	}
	
	return nil
}