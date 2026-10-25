package database

// 驱动基准：用于评估纯 Go 驱动（modernc.org/sqlite）相对 CGO 驱动
// （github.com/mattn/go-sqlite3）的开销，不参与常规测试。
// 运行：go test ./internal/database -bench . -benchtime 2000x
// 参考值（Windows / FULL 同步 / 2000 次，同机前后测量）：
//   CGO  mattn v1.14.50   INSERT 5.75ms   点查 97.9µs
//   pure modernc v1.44.3  INSERT 6.46ms   点查 104.1µs
//   pure modernc v1.61.0  INSERT 7.29ms   点查 116.8µs
// 绝对值随磁盘 fsync 性能波动较大，看量级即可：瓶颈在 fsync 与查询计划，
// 不在驱动本身（本项目写量很低，实际接口延迟无可见差异）。

import (
	"fmt"
	"path/filepath"
	"testing"

	coredatabase "permaporter/internal/core/database"
)

func benchDB(b *testing.B) *DB {
	b.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(b.TempDir(), "b.db")) +
		"?_busy_timeout=10000&_foreign_keys=1&_journal_mode=DELETE"
	store, err := coredatabase.Open(dsn)
	if err != nil {
		b.Fatal(err)
	}
	db := Attach(store)
	b.Cleanup(func() { db.Close(); _ = store.Close() })
	return db
}

func BenchmarkInsertPackage(b *testing.B) {
	db := benchDB(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := db.Exec(`INSERT INTO npm_packages (pkg_name, description) VALUES (?, ?)`,
			fmt.Sprintf("pkg-%d", i), "bench"); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkQueryPackage(b *testing.B) {
	db := benchDB(b)
	for i := 0; i < 1000; i++ {
		if _, err := db.Exec(`INSERT INTO npm_packages (pkg_name, description) VALUES (?, ?)`,
			fmt.Sprintf("pkg-%d", i), "bench"); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		var name string
		if err := db.QueryRow(`SELECT pkg_name FROM npm_packages WHERE pkg_name = ?`,
			fmt.Sprintf("pkg-%d", i%1000)).Scan(&name); err != nil {
			b.Fatal(err)
		}
	}
}
