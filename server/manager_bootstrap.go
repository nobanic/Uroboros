package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	arsenalrunner "github.com/nobanic/UroborOS/arsenal"
	pgdb "github.com/nobanic/UroborOS/db"
	"github.com/nobanic/UroborOS/enrich"
	"github.com/nobanic/UroborOS/intercept"
	katanarunner "github.com/nobanic/UroborOS/katana"
	"github.com/nobanic/UroborOS/traffic"
)

// managerStartup owns acquisition until the complete manager is returned.
// Recovery failures keep their original explicit cleanup boundary; optional
// traffic startup and logging-table errors retain their best-effort behavior.
type managerStartup struct {
	root      string
	proxyAddr string
	runtime   *Manager
}

func (startup *managerStartup) start() (*Manager, error) {
	if abs, err := filepath.Abs(startup.root); err == nil {
		startup.root = abs
	}
	if err := os.MkdirAll(startup.root, 0o755); err != nil {
		return nil, err
	}
	pg, err := startup.openPersistence()
	if err != nil {
		return nil, err
	}
	startup.runtime = &Manager{dir: startup.root, pg: pg}
	startup.runtime.assets = pg.Assets()
	startup.runtime.tasks = make(map[string]*Task)
	startup.runtime.interceptor = intercept.New(pg)
	if err := startup.openTraffic(); err != nil {
		return nil, err
	}
	startup.loadSettings()
	startup.startRunners()
	startup.runtime.syncBrowserMCPProxy()
	return startup.runtime, nil
}

func (startup *managerStartup) openPersistence() (*pgdb.DB, error) {
	dsn, source, err := pgdb.DSN()
	if err != nil {
		return nil, err
	}
	log.Printf("[pg] database config source: %s", source)
	pg, err := pgdb.Open(dsn)
	if err != nil {
		return nil, err
	}
	if err := pg.RecoverFindingRetests(); err != nil {
		pg.Close()
		return nil, fmt.Errorf("recover finding retests: %w", err)
	}
	if err := pg.EnsureLLMRecordsTable(); err != nil {
		log.Printf("[llmrec] create table: %v", err)
	}
	if err := pg.EnsureLLMUsageTable(); err != nil {
		log.Printf("[llmusage] create table: %v", err)
	}
	return pg, nil
}

func (startup *managerStartup) openTraffic() error {
	if startup.proxyAddr == "" {
		return nil
	}
	m := startup.runtime
	pg, proxyAddr := m.pg, startup.proxyAddr
	tr, err := traffic.Open(filepath.Join(startup.root, "traffic"), proxyAddr)
	if err != nil {
		log.Printf("[traffic] disabled: %v", err)
	} else {
		err = tr.RecoverHostDeleteStages(func(_ int64, taskID int64) (bool, error) {
			if taskID <= 0 {
				return false, errors.New("archived traffic staging log is missing a task ID")
			}
			task, taskErr := pg.GetTask(taskID)
			if taskErr != nil {
				return false, taskErr
			}
			return task == nil, nil
		})
		if err != nil {
			_ = tr.Close()
			_ = pg.Close()
			return fmt.Errorf("recover traffic delete staging: %w", err)
		}
	}
	if tr != nil {
		m.traffic = tr
		go func() {
			log.Printf("[traffic] recording proxy on %s (set HTTP_PROXY=%s + trust _ca CA)", proxyAddr, tr.ProxyAddr())
			if err := tr.Start(); err != nil {
				log.Printf("[traffic] proxy stopped: %v", err)
			}
		}()
	}
	return nil
}

func (startup *managerStartup) loadSetting(key string, destination *string) {
	value, present, _ := startup.runtime.pg.GetSetting(key)
	if present {
		*destination = value
	}
}

func (startup *managerStartup) loadSettings() {
	runtime, db := startup.runtime, startup.runtime.pg
	runtime.trafficOn, runtime.llmRecOn, runtime.webSearchOn =
		db.GetBool(settingTrafficCapture, false), db.GetBool(settingLLMRecord, false), db.GetBool(settingWebSearchOn, defaultWebSearchEnabled)
	startup.loadSetting(settingWebSearchBackend, &runtime.webSearchBackend)
	if runtime.webSearchBackend == "" {
		runtime.webSearchBackend = defaultWebSearchBackend
	}
	startup.loadSetting(settingBraveKey, &runtime.braveKey)
	startup.loadSetting(settingTavilyKey, &runtime.tavilyKey)
	startup.loadSetting(settingWebSearchProxy, &runtime.webSearchProxy)
	startup.loadSetting(settingGlobalProxy, &runtime.globalProxy)
	runtime.globalProxy = strings.TrimSpace(runtime.globalProxy)
	if runtime.traffic != nil {
		if err := runtime.traffic.SetUpstreamProxy(runtime.globalProxy); err != nil {
			log.Printf("[proxy] global proxy %q is invalid, ignored: %v", runtime.globalProxy, err)
		}
	}
}

func (startup *managerStartup) startRunners() {
	m := startup.runtime
	m.enrich = enrich.New(m.assets, m.ProxyAddr, 4)
	katanaCtx, katanaStop := context.WithCancelCause(context.Background())
	m.katanaStop = katanaStop
	m.katana = katanarunner.NewManager(katanaCtx, m.pg, m.assets, m.traffic, katanarunner.ManagerConfig{})
	arsenalCtx, arsenalStop := context.WithCancelCause(context.Background())
	m.arsenalStop = arsenalStop
	m.arsenal = arsenalrunner.NewManager(arsenalCtx, m.pg, m.assets, m.traffic, arsenalrunner.ManagerConfig{})
}
