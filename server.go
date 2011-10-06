package zk

import (
	"bufio"
	"bytes"
	"fmt"
	"io/ioutil"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Server represents a ZooKeeper server, its data and configuration files.
type Server struct {
	runDir string
	zkDir  string
}

// CreateServer creates the directory runDir and sets up a ZooKeeper server
// environment inside it. It is an error if runDir already exists.
// The server will listen on the specified TCP port.
// 
// The ZooKeeper installation directory is specified by zkDir.
// If this is empty, a system default will be used.
//
// CreateServer does not start the server.
func CreateServer(port int, runDir, zkDir string) (*Server, os.Error) {
	if err := os.Mkdir(runDir, 0777); err != nil {
		return nil, err
	}
	srv := &Server{runDir: runDir, zkDir: zkDir}
	if err := srv.writeLog4JConfig(); err != nil {
		return nil, err
	}
	if err := srv.writeZooKeeperConfig(port); err != nil {
		return nil, err
	}
	if err := srv.writeInstallDir(); err != nil {
		return nil, err
	}
	return srv, nil
}

// AttachServer creates a new ZooKeeper Server instance
// to operate inside an existing run directory, runDir.
// The directory must have been created with CreateServer.
func AttachServer(runDir string) (*Server, os.Error) {
	srv := &Server{runDir: runDir}
	if err := srv.readInstallDir(); err != nil {
		return nil, fmt.Errorf("cannot read server install directory: %v", err)
	}
	return srv, nil
}

func (srv *Server) checkAvailability() os.Error {
	port, err := srv.networkPort()
	if err != nil {
		return fmt.Errorf("cannot get network port: %v", err)
	}
	l, err := net.Listen("tcp", fmt.Sprintf("localhost:%d", port))
	if err != nil {
		return fmt.Errorf("cannot listen on port %v: %v", port, err)
	}
	l.Close()
	return nil
}

// NetworkPort returns the TCP port number that
// the server is configured for.
func (srv *Server) networkPort() (int, os.Error) {
	f, err := os.Open(srv.path("zoo.cfg"))
	if err != nil {
		return 0, err
	}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadSlice('\n')
		if err != nil {
			return 0, fmt.Errorf("cannot get port from %q", srv.path("zoo.cfg"))
		}
		var port int
		if n, _ := fmt.Sscanf(string(line), "clientPort=%d\n", &port); n == 1 {
			return port, nil
		}
	}
	panic("not reached")
}

// ServerCommand returns the command used to start the
// ZooKeeper server. It is provided for debugging and testing
// purposes only.
func (srv *Server) command() ([]string, os.Error) {
	cp, err := srv.classPath()
	if err != nil {
		return nil, fmt.Errorf("cannot get class path: %v", err)
	}
	return []string{
		"java",
		"-cp", strings.Join(cp, ":"),
		"-Dzookeeper.root.logger=INFO,CONSOLE",
		"-Dlog4j.configuration=file:" + srv.path("log4j.properties"),
		"org.apache.zookeeper.server.quorum.QuorumPeerMain",
		srv.path("zoo.cfg"),
	}, nil
}

var log4jProperties = `
log4j.rootLogger=INFO, CONSOLE
log4j.appender.CONSOLE=org.apache.log4j.ConsoleAppender
log4j.appender.CONSOLE.Threshold=INFO
log4j.appender.CONSOLE.layout=org.apache.log4j.PatternLayout
log4j.appender.CONSOLE.layout.ConversionPattern=%d{ISO8601} - %-5p [%t:%C{1}@%L] - %m%n
`

func (srv *Server) writeLog4JConfig() (err os.Error) {
	return ioutil.WriteFile(srv.path("log4j.properties"), []byte(log4jProperties), 0666)
}

func (srv *Server) writeZooKeeperConfig(port int) (err os.Error) {
	return ioutil.WriteFile(srv.path("zoo.cfg"), []byte(fmt.Sprintf(
		"tickTime=2000\n"+
			"dataDir=%s\n"+
			"clientPort=%d\n"+
			"maxClientCnxns=500\n",
		srv.runDir, port)), 0666)
}

func (srv *Server) writeInstallDir() os.Error {
	return ioutil.WriteFile(srv.path("installdir.txt"), []byte(srv.zkDir+"\n"), 0666)
}

func (srv *Server) readInstallDir() os.Error {
	data, err := ioutil.ReadFile(srv.path("installdir.txt"))
	if err != nil {
		return err
	}
	if data[len(data)-1] == '\n' {
		data = data[0 : len(data)-1]
	}
	srv.zkDir = string(data)
	return nil
}

func (srv *Server) classPath() ([]string, os.Error) {
	dir := srv.zkDir
	if dir == "" {
		return systemClassPath()
	}
	if err := checkDirectory(dir); err != nil {
		return nil, err
	}
	// Two possibilities, as seen in zkEnv.sh:
	// 1) locally built binaries (jars are in build directory)
	// 2) release binaries
	if build := filepath.Join(dir, "build"); checkDirectory(build) == nil {
		dir = build
	}
	classPath, err := filepath.Glob(filepath.Join(dir, "zookeeper-*.jar"))
	if err != nil {
		panic(fmt.Errorf("glob for jar files: %v", err))
	}
	more, err := filepath.Glob(filepath.Join(dir, "lib/*.jar"))
	if err != nil {
		panic(fmt.Errorf("glob for lib jar files: %v", err))
	}

	classPath = append(classPath, more...)
	if len(classPath) == 0 {
		return nil, fmt.Errorf("zookeeper libraries not found in %q", dir)
	}
	return classPath, nil
}

const zookeeperEnviron = "/etc/zookeeper/conf/environment"

func systemClassPath() ([]string, os.Error) {
	f, err := os.Open(zookeeperEnviron)
	if f == nil {
		return nil, err
	}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadSlice('\n')
		if err != nil {
			break
		}
		if !bytes.HasPrefix(line, []byte("CLASSPATH=")) {
			continue
		}

		// remove variable and newline
		path := string(line[len("CLASSPATH=") : len(line)-1])

		// trim white space
		path = strings.Trim(path, " \t\r")

		// strip quotes
		if path[0] == '"' {
			path = path[1 : len(path)-1]
		}

		// split on :
		classPath := strings.Split(path, ":")

		// split off $ZOOCFGDIR
		if len(classPath) > 0 && classPath[0] == "$ZOOCFGDIR" {
			classPath = classPath[1:]
		}

		if len(classPath) == 0 {
			return nil, fmt.Errorf("empty class path in %q", zookeeperEnviron)
		}
		return classPath, nil
	}
	return nil, fmt.Errorf("no class path found in %q", zookeeperEnviron)
}

// checkDirectory returns an error if the given path
// does not exist or is not a directory.
func checkDirectory(path string) os.Error {
	if info, err := os.Stat(path); err != nil || !info.IsDirectory() {
		if err == nil {
			err = &os.PathError{Op: "stat", Path: path, Error: os.NewError("is not a directory")}
		}
		return err
	}
	return nil
}

func (srv *Server) path(name string) string {
	return filepath.Join(srv.runDir, name)
}
