package db

import (
	"sync/atomic"

	"gorm.io/gorm"
)

// Store binds all persistence operations to one database. The caller owns its lifetime.
type Store struct {
	conn *gorm.DB
}

func NewStore(conn *gorm.DB) *Store {
	return &Store{conn: conn}
}

var defaultStore atomic.Pointer[Store]

// Default returns the application's shared database store. Initialize it before
// starting request handlers or background jobs.
func Default() *Store {
	store := defaultStore.Load()
	if store == nil {
		panic("db: default store is not initialized")
	}
	return store
}

// Initialized reports whether a default store has been installed.
//
// 后台任务会在测试或早期启动阶段被调用，它们需要先判断能否安全访问数据库，
// 而不是让 Default 直接 panic。
func Initialized() bool {
	return defaultStore.Load() != nil
}

// SetDefault installs the application store and returns the previous value.
// Use it during startup, or in serial tests that restore the previous store
// after their workers finish. Replacing a store while jobs run is unsupported.
// Passing nil clears the default. The caller owns the database's lifetime.
func SetDefault(store *Store) *Store {
	return defaultStore.Swap(store)
}
