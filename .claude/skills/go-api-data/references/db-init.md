# DB 연결 초기화 패턴

## GORM + PostgreSQL

```go
// internal/config/database.go
package config

import (
    "fmt"
    "gorm.io/driver/postgres"
    "gorm.io/gorm"
    "gorm.io/gorm/logger"
)

func NewDB(cfg DatabaseConfig) (*gorm.DB, error) {
    logMode := logger.Silent
    if cfg.Debug {
        logMode = logger.Info
    }

    db, err := gorm.Open(postgres.Open(cfg.DSN), &gorm.Config{
        Logger: logger.Default.LogMode(logMode),
    })
    if err != nil {
        return nil, fmt.Errorf("failed to connect database: %w", err)
    }

    sqlDB, err := db.DB()
    if err != nil {
        return nil, err
    }
    sqlDB.SetMaxOpenConns(cfg.MaxConns)
    sqlDB.SetMaxIdleConns(cfg.MaxConns / 2)

    return db, nil
}
```

## GORM + MySQL

```go
import "gorm.io/driver/mysql"

db, err := gorm.Open(mysql.Open(cfg.DSN), &gorm.Config{})
```

## sqlx + PostgreSQL

```go
import (
    "github.com/jmoiron/sqlx"
    _ "github.com/lib/pq"
)

func NewSqlxDB(cfg DatabaseConfig) (*sqlx.DB, error) {
    db, err := sqlx.Connect("postgres", cfg.DSN)
    if err != nil {
        return nil, fmt.Errorf("failed to connect: %w", err)
    }
    db.SetMaxOpenConns(cfg.MaxConns)
    return db, nil
}
```

## 마이그레이션 실행 (golang-migrate)

```go
import (
    "github.com/golang-migrate/migrate/v4"
    _ "github.com/golang-migrate/migrate/v4/database/postgres"
    _ "github.com/golang-migrate/migrate/v4/source/file"
)

func RunMigrations(dsn string) error {
    m, err := migrate.New("file://migrations", dsn)
    if err != nil {
        return err
    }
    if err := m.Up(); err != nil && err != migrate.ErrNoChange {
        return err
    }
    return nil
}
```

## DSN 형식

**PostgreSQL:** `"host=localhost user=postgres password=secret dbname=mydb port=5432 sslmode=disable"`
**MySQL:** `"user:password@tcp(localhost:3306)/dbname?charset=utf8mb4&parseTime=True&loc=Local"`
**SQLite:** `"file:test.db?cache=shared"`
