
package dashboard

//go:generate npm --prefix ./assets install
//go:generate ./assets/node_modules/.bin/webpack --config ./assets/webpack.config.js --context ./assets
//go:generate go-bindata -nometadata -o assets.go -prefix assets -nocompress -pkg dashboard assets/dashboard.html assets/bundle.js
//go:generate sh -c "sed 's#var _bundleJs#//nolint:misspell\\\n&#' assets.go > assets.go.tmp && mv assets.go.tmp assets.go"
//go:generate sh -c "sed 's#var _dashboardHtml#//nolint:misspell\\\n&#' assets.go > assets.go.tmp && mv assets.go.tmp assets.go"
//go:generate gofmt -w -s assets.go

import (
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/elastic/gosigar"
	"github.com/epvchain/go-epvchain/book"
	"github.com/epvchain/go-epvchain/peer"
	"github.com/epvchain/go-epvchain/content"
	"github.com/epvchain/go-epvchain/remote"
	"github.com/rcrowley/go-metrics"
	"golang.org/x/net/websocket"
)

const (
	activeMemorySampleLimit   = 200 
	virtualMemorySampleLimit  = 200 
	networkIngressSampleLimit = 200 
	networkEgressSampleLimit  = 200 
	processCPUSampleLimit     = 200 
	systemCPUSampleLimit      = 200 
	diskReadSampleLimit       = 200 
	diskWriteSampleLimit      = 200 
)

var nextID uint32 

type Dashboard struct {
	config *Config

	listener net.Listener
	conns    map[uint32]*client 
	charts   *HomeMessage
	commit   string
	lock     sync.RWMutex 

	quit chan chan error 
	wg   sync.WaitGroup
}

type client struct {
	conn   *websocket.Conn 
	msg    chan Message    
	logger log.Logger      
}

func New(config *Config, commit string) (*Dashboard, error) {
	now := time.Now()
	db := &Dashboard{
		conns:  make(map[uint32]*client),
		config: config,
		quit:   make(chan chan error),
		charts: &HomeMessage{
			ActiveMemory:   emptyChartEntries(now, activeMemorySampleLimit, config.Refresh),
			VirtualMemory:  emptyChartEntries(now, virtualMemorySampleLimit, config.Refresh),
			NetworkIngress: emptyChartEntries(now, networkIngressSampleLimit, config.Refresh),
			NetworkEgress:  emptyChartEntries(now, networkEgressSampleLimit, config.Refresh),
			ProcessCPU:     emptyChartEntries(now, processCPUSampleLimit, config.Refresh),
			SystemCPU:      emptyChartEntries(now, systemCPUSampleLimit, config.Refresh),
			DiskRead:       emptyChartEntries(now, diskReadSampleLimit, config.Refresh),
			DiskWrite:      emptyChartEntries(now, diskWriteSampleLimit, config.Refresh),
		},
		commit: commit,
	}
	return db, nil
}

func emptyChartEntries(t time.Time, limit int, refresh time.Duration) ChartEntries {
	ce := make(ChartEntries, limit)
	for i := 0; i < limit; i++ {
		ce[i] = &ChartEntry{
			Time: t.Add(-time.Duration(i) * refresh),
		}
	}
	return ce
}

func (db *Dashboard) Protocols() []p2p.Protocol { return nil }

func (db *Dashboard) APIs() []rpc.API { return nil }

func (db *Dashboard) Start(server *p2p.Server) error {
	log.Info("Starting dashboard")

	db.wg.Add(2)
	go db.collectData()
	go db.collectLogs() 

	http.HandleFunc("/", db.webHandler)
	http.Handle("/api", websocket.Handler(db.apiHandler))

	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", db.config.Host, db.config.Port))
	if err != nil {
		return err
	}
	db.listener = listener

	go http.Serve(listener, nil)

	return nil
}

func (db *Dashboard) Stop() error {

	var errs []error
	if err := db.listener.Close(); err != nil {
		errs = append(errs, err)
	}

	errc := make(chan error, 1)
	for i := 0; i < 2; i++ {
		db.quit <- errc
		if err := <-errc; err != nil {
			errs = append(errs, err)
		}
	}

	db.lock.Lock()
	for _, c := range db.conns {
		if err := c.conn.Close(); err != nil {
			c.logger.Warn("Failed to close connection", "err", err)
		}
	}
	db.lock.Unlock()

	db.wg.Wait()
	log.Info("Dashboard stopped")

	var err error
	if len(errs) > 0 {
		err = fmt.Errorf("%v", errs)
	}

	return err
}

func (db *Dashboard) webHandler(w http.ResponseWriter, r *http.Request) {
	log.Debug("Request", "URL", r.URL)

	path := r.URL.String()
	if path == "/" {
		path = "/dashboard.html"
	}

	if db.config.Assets != "" {
		blob, err := ioutil.ReadFile(filepath.Join(db.config.Assets, path))
		if err != nil {
			log.Warn("Failed to read file", "path", path, "err", err)
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Write(blob)
		return
	}
	blob, err := Asset(path[1:])
	if err != nil {
		log.Warn("Failed to load the asset", "path", path, "err", err)
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.Write(blob)
}

func (db *Dashboard) apiHandler(conn *websocket.Conn) {
	id := atomic.AddUint32(&nextID, 1)
	client := &client{
		conn:   conn,
		msg:    make(chan Message, 128),
		logger: log.New("id", id),
	}
	done := make(chan struct{})

	db.wg.Add(1)
	go func() {
		defer db.wg.Done()

		for {
			select {
			case <-done:
				return
			case msg := <-client.msg:
				if err := websocket.JSON.Send(client.conn, msg); err != nil {
					client.logger.Warn("Failed to send the message", "msg", msg, "err", err)
					client.conn.Close()
					return
				}
			}
		}
	}()

	versionMeta := ""
	if len(params.VersionMeta) > 0 {
		versionMeta = fmt.Sprintf(" (%s)", params.VersionMeta)
	}

	client.msg <- Message{
		General: &GeneralMessage{
			Version: fmt.Sprintf("v%d.%d.%d%s", params.VersionMajor, params.VersionMinor, params.VersionPatch, versionMeta),
			Commit:  db.commit,
		},
		Home: &HomeMessage{
			ActiveMemory:   db.charts.ActiveMemory,
			VirtualMemory:  db.charts.VirtualMemory,
			NetworkIngress: db.charts.NetworkIngress,
			NetworkEgress:  db.charts.NetworkEgress,
			ProcessCPU:     db.charts.ProcessCPU,
			SystemCPU:      db.charts.SystemCPU,
			DiskRead:       db.charts.DiskRead,
			DiskWrite:      db.charts.DiskWrite,
		},
	}

	db.lock.Lock()
	db.conns[id] = client
	db.lock.Unlock()
	defer func() {
		db.lock.Lock()
		delete(db.conns, id)
		db.lock.Unlock()
	}()
	for {
		fail := []byte{}
		if _, err := conn.Read(fail); err != nil {
			close(done)
			return
		}

	}
}

func (db *Dashboard) collectData() {
	defer db.wg.Done()
	systemCPUUsage := gosigar.Cpu{}
	systemCPUUsage.Get()
	var (
		prevNetworkIngress = metrics.DefaultRegistry.Get("p2p/InboundTraffic").(metrics.Meter).Count()
		prevNetworkEgress  = metrics.DefaultRegistry.Get("p2p/OutboundTraffic").(metrics.Meter).Count()
		prevProcessCPUTime = getProcessCPUTime()
		prevSystemCPUUsage = systemCPUUsage
		prevDiskRead       = metrics.DefaultRegistry.Get("epv/db/chaindata/compact/input").(metrics.Meter).Count()
		prevDiskWrite      = metrics.DefaultRegistry.Get("epv/db/chaindata/compact/output").(metrics.Meter).Count()

		frequency = float64(db.config.Refresh / time.Second)
		numCPU    = float64(runtime.NumCPU())
	)

	for {
		select {
		case errc := <-db.quit:
			errc <- nil
			return
		case <-time.After(db.config.Refresh):
			systemCPUUsage.Get()
			var (
				curNetworkIngress = metrics.DefaultRegistry.Get("p2p/InboundTraffic").(metrics.Meter).Count()
				curNetworkEgress  = metrics.DefaultRegistry.Get("p2p/OutboundTraffic").(metrics.Meter).Count()
				curProcessCPUTime = getProcessCPUTime()
				curSystemCPUUsage = systemCPUUsage
				curDiskRead       = metrics.DefaultRegistry.Get("epv/db/chaindata/compact/input").(metrics.Meter).Count()
				curDiskWrite      = metrics.DefaultRegistry.Get("epv/db/chaindata/compact/output").(metrics.Meter).Count()

				deltaNetworkIngress = float64(curNetworkIngress - prevNetworkIngress)
				deltaNetworkEgress  = float64(curNetworkEgress - prevNetworkEgress)
				deltaProcessCPUTime = curProcessCPUTime - prevProcessCPUTime
				deltaSystemCPUUsage = systemCPUUsage.Delta(prevSystemCPUUsage)
				deltaDiskRead       = curDiskRead - prevDiskRead
				deltaDiskWrite      = curDiskWrite - prevDiskWrite
			)
			prevNetworkIngress = curNetworkIngress
			prevNetworkEgress = curNetworkEgress
			prevProcessCPUTime = curProcessCPUTime
			prevSystemCPUUsage = curSystemCPUUsage
			prevDiskRead = curDiskRead
			prevDiskWrite = curDiskWrite

			now := time.Now()

			var mem runtime.MemStats
			runtime.ReadMemStats(&mem)
			activeMemory := &ChartEntry{
				Time:  now,
				Value: float64(mem.Alloc) / frequency,
			}
			virtualMemory := &ChartEntry{
				Time:  now,
				Value: float64(mem.Sys) / frequency,
			}
			networkIngress := &ChartEntry{
				Time:  now,
				Value: deltaNetworkIngress / frequency,
			}
			networkEgress := &ChartEntry{
				Time:  now,
				Value: deltaNetworkEgress / frequency,
			}
			processCPU := &ChartEntry{
				Time:  now,
				Value: deltaProcessCPUTime / frequency / numCPU * 100,
			}
			systemCPU := &ChartEntry{
				Time:  now,
				Value: float64(deltaSystemCPUUsage.Sys+deltaSystemCPUUsage.User) / frequency / numCPU,
			}
			diskRead := &ChartEntry{
				Time:  now,
				Value: float64(deltaDiskRead) / frequency,
			}
			diskWrite := &ChartEntry{
				Time:  now,
				Value: float64(deltaDiskWrite) / frequency,
			}
			db.charts.ActiveMemory = append(db.charts.ActiveMemory[1:], activeMemory)
			db.charts.VirtualMemory = append(db.charts.VirtualMemory[1:], virtualMemory)
			db.charts.NetworkIngress = append(db.charts.NetworkIngress[1:], networkIngress)
			db.charts.NetworkEgress = append(db.charts.NetworkEgress[1:], networkEgress)
			db.charts.ProcessCPU = append(db.charts.ProcessCPU[1:], processCPU)
			db.charts.SystemCPU = append(db.charts.SystemCPU[1:], systemCPU)
			db.charts.DiskRead = append(db.charts.DiskRead[1:], diskRead)
			db.charts.DiskWrite = append(db.charts.DiskRead[1:], diskWrite)

			db.sendToAll(&Message{
				Home: &HomeMessage{
					ActiveMemory:   ChartEntries{activeMemory},
					VirtualMemory:  ChartEntries{virtualMemory},
					NetworkIngress: ChartEntries{networkIngress},
					NetworkEgress:  ChartEntries{networkEgress},
					ProcessCPU:     ChartEntries{processCPU},
					SystemCPU:      ChartEntries{systemCPU},
					DiskRead:       ChartEntries{diskRead},
					DiskWrite:      ChartEntries{diskWrite},
				},
			})
		}
	}
}

func (db *Dashboard) collectLogs() {
	defer db.wg.Done()

	id := 1

	for {
		select {
		case errc := <-db.quit:
			errc <- nil
			return
		case <-time.After(db.config.Refresh / 2):
			db.sendToAll(&Message{
				Logs: &LogsMessage{
					Log: []string{fmt.Sprintf("%-4d: This is a fake log.", id)},
				},
			})
			id++
		}
	}
}

func (db *Dashboard) sendToAll(msg *Message) {
	db.lock.Lock()
	for _, c := range db.conns {
		select {
		case c.msg <- *msg:
		default:
			c.conn.Close()
		}
	}
	db.lock.Unlock()
}
