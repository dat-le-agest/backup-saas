package infra

import (
	"fmt"
	"net/http"
	"runtime"
	"strings"
	"time"

	"core/httpx"
	"core/response"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

var startTime = time.Now()

type Stats struct {
	UptimeSeconds int64        `json:"uptime_seconds"`
	Runtime       RuntimeStats `json:"runtime"`
	Database      *DBStats     `json:"database,omitempty"`
	Redis         *RedisStats  `json:"redis,omitempty"`
}

type RuntimeStats struct {
	NumGoroutine int    `json:"num_goroutine"`
	NumCPU       int    `json:"num_cpu"`
	AllocMB      string `json:"alloc_mb"`
	TotalAllocMB string `json:"total_alloc_mb"`
	SysMB        string `json:"sys_mb"`
	NumGC        uint32 `json:"num_gc"`
}

type DBStats struct {
	Up           bool   `json:"up"`
	PingMS       int64  `json:"ping_ms"`
	OpenConns    int    `json:"open_conns"`
	InUse        int    `json:"in_use"`
	Idle         int    `json:"idle"`
	MaxOpenConns int    `json:"max_open_conns"`
	WaitCount    int64  `json:"wait_count"`
	WaitDuration int64  `json:"wait_duration_ms"`
	DatabaseSize string `json:"database_size,omitempty"`
}

type RedisStats struct {
	Up               bool   `json:"up"`
	PingMS           int64  `json:"ping_ms"`
	ConnectedClients string `json:"connected_clients,omitempty"`
	UsedMemoryHuman  string `json:"used_memory_human,omitempty"`
	PeakMemoryHuman  string `json:"peak_memory_human,omitempty"`
	TotalKeys        int64  `json:"total_keys"`
}

// Probe collects live infrastructure diagnostics.
type Probe struct {
	db    *gorm.DB
	redis *redis.Client
	key   string
}

func NewProbe(db *gorm.DB, rdb *redis.Client, key string) *Probe {
	return &Probe{db: db, redis: rdb, key: key}
}

func (p *Probe) RegisterRoutes(r gin.IRouter) {
	r.GET("/admin/v1/infra", httpx.AdminKey(p.key), p.handle)
}

func (p *Probe) handle(c *gin.Context) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	s := Stats{
		UptimeSeconds: int64(time.Since(startTime).Seconds()),
		Runtime: RuntimeStats{
			NumGoroutine: runtime.NumGoroutine(),
			NumCPU:       runtime.NumCPU(),
			AllocMB:      fmt.Sprintf("%.2f MB", float64(mem.Alloc)/1024/1024),
			TotalAllocMB: fmt.Sprintf("%.2f MB", float64(mem.TotalAlloc)/1024/1024),
			SysMB:        fmt.Sprintf("%.2f MB", float64(mem.Sys)/1024/1024),
			NumGC:        mem.NumGC,
		},
	}

	if p.db != nil {
		sqlDB, err := p.db.DB()
		if err == nil {
			t0 := time.Now()
			pingErr := sqlDB.PingContext(c.Request.Context())
			pingMS := time.Since(t0).Milliseconds()
			st := sqlDB.Stats()
			dbStat := &DBStats{
				Up:           pingErr == nil,
				PingMS:       pingMS,
				OpenConns:    st.OpenConnections,
				InUse:        st.InUse,
				Idle:         st.Idle,
				MaxOpenConns: st.MaxOpenConnections,
				WaitCount:    st.WaitCount,
				WaitDuration: st.WaitDuration.Milliseconds(),
			}
			var size string
			_ = p.db.WithContext(c.Request.Context()).Raw("SELECT pg_size_pretty(pg_database_size(current_database()))").Scan(&size)
			dbStat.DatabaseSize = size
			s.Database = dbStat
		}
	}

	if p.redis != nil {
		t0 := time.Now()
		pingErr := p.redis.Ping(c.Request.Context()).Err()
		pingMS := time.Since(t0).Milliseconds()
		redStat := &RedisStats{
			Up:     pingErr == nil,
			PingMS: pingMS,
		}
		if pingErr == nil {
			if keys, err := p.redis.DBSize(c.Request.Context()).Result(); err == nil {
				redStat.TotalKeys = keys
			}
			if info, err := p.redis.Info(c.Request.Context(), "memory", "clients").Result(); err == nil {
				for _, line := range strings.Split(info, "\r\n") {
					parts := strings.SplitN(line, ":", 2)
					if len(parts) == 2 {
						switch parts[0] {
						case "used_memory_human":
							redStat.UsedMemoryHuman = parts[1]
						case "used_memory_peak_human":
							redStat.PeakMemoryHuman = parts[1]
						case "connected_clients":
							redStat.ConnectedClients = parts[1]
						}
					}
				}
			}
		}
		s.Redis = redStat
	}

	response.OK(c, http.StatusOK, s)
}
