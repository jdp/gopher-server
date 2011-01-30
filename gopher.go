package main

import (
	"bufio"
	"bytes"
	"container/vector"
	"encoding/line"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"path"
	"regexp"
	"reflect"
	"strconv"
	"strings"
)

// Context represents the union of the re
type Context struct {
	conn net.Conn
	Request string
}

// Write sends raw <CR><LF> terminated data to the client
func (ctx *Context) Write(data string) (n int, err os.Error) {
	n, err = fmt.Fprintf(ctx.conn, "%s\r\n", data)
	return
}

// Info sends an info-formatted string to the client
func (s *Server) InfoLine(line string) string {
	return fmt.Sprintf("i%s\tF\t%s\t%d", line, s.Hostname, s.Port)
}

func (s *Server) TextfileLine(name string, path string) string {
	return fmt.Sprintf("0%s\t/%s\t%s\t%d", name, path, s.Hostname, s.Port)
}

func (s *Server) DirectoryLine(name string, path string) string {
	return fmt.Sprintf("1%s\t/%s\t%s\t%d", name, path, s.Hostname, s.Port)
}

// Error sends an error-formatted string to the client
func (ctx *Context) Error(line string) (n int, err os.Error) {
	n, err = fmt.Fprintf(ctx.conn, "3%s\terror\thost\t0\r\n", line)
	return
}

type gophermapEntry struct {
	Type byte
	Data string
	Path string
	Host string
	Port int
}

func (entry *gophermapEntry) ToString() string {
	return fmt.Sprintf("%c%s\t%s\t%s\t%d", entry.Type, entry.Data, entry.Path, entry.Host, entry.Port)
}

func (s *Server) ParseGophermapLine(ctx *Context, line string) (entry *gophermapEntry) {
	entry = &gophermapEntry{Type: line[0]}
	parts := strings.Split(line[1:], "\t", 4)
	if len(parts) > 0 {
		entry.Data = parts[0]
	} else {
		entry.Data = ""
	}
	if len(parts) > 1 {
		if (strings.HasPrefix(parts[1], "/")) {
			entry.Path = parts[1]
		} else {
			entry.Path = ctx.Request+"/"+parts[1]
		}
	} else {
		entry.Path = ctx.Request+"/"+parts[0]
	}
	if len(parts) > 2 {
		entry.Host = parts[2]
	} else {
		entry.Host = s.Hostname
	}
	if len(parts) > 3 {
		port, _ := strconv.Atoi(parts[3])
		entry.Port = port
	} else {
		entry.Port = s.Port
	}
	return entry
}

func (s *Server) Gophermap(ctx *Context, gmap *os.File, dir *os.File) (ok bool, err os.Error) {
	cwd := dir.Name()[len(s.Cwd):]
	linereader := line.NewReader(bufio.NewReader(gmap), 512)
	for {
		if read, _, err := linereader.ReadLine(); err == nil {
			entry := bytes.NewBuffer(read).String()
			if strings.Index(entry, "\t") == -1 {
				ctx.Write(s.InfoLine(entry))
			} else {
				ctx.Write(s.ParseGophermapLine(ctx, entry).ToString())
			}
		} else {
			if err != os.EOF {
				return false, err
			}
			break
		}
		
	}
	ctx.Write(".")
	s.Logger.Printf("Served gophermapped directory `%s`\n", cwd)
	return true, nil
}

// Directory sends a Gopher listing of the directory specified
// If a gophermap file is present, it is used instead of listing the directory contents
func (s *Server) Directory(ctx *Context, dir *os.File) (ok bool, err os.Error) {
	cwd := dir.Name()[len(s.Cwd):]
	if mapfile, maperr := os.Open(dir.Name()+"/gophermap", 0, 0); maperr == nil {
		defer mapfile.Close()
		s.Gophermap(ctx, mapfile, dir)
		ok = true
	} else {
		entries, err := dir.Readdir(-1)
		if err != nil {
			s.Logger.Printf("Could not show directory: `%s'\n", err)
			return
		}
		for _, entry := range entries {
			expandedName := strings.Trim(fmt.Sprintf("%s/%s", cwd, entry.Name), "/")
			switch true {
			case entry.IsRegular():
				_, err = ctx.Write(s.TextfileLine(entry.Name, expandedName))
			case entry.IsDirectory():
				_, err = ctx.Write(s.DirectoryLine(entry.Name, expandedName))
			default:
				_, err = ctx.Write(s.InfoLine(entry.Name))
			}
		}
		s.Logger.Printf("Served directory `%s'\n", cwd);
		ctx.Write(".")
		ok = true
	}
	return
}

func (s *Server) Textfile(ctx *Context, file *os.File) (ok bool, err os.Error) {
	const BUFSIZE = 512
	var buf [BUFSIZE]byte
	for {
		switch nr, er := file.Read(buf[:]); true {
		case nr < 0:
			s.Logger.Printf("Error reading from text file `%s': %s\n", ctx.Request, er)
			err = er
			return
		case nr == 0:
			s.Logger.Printf("Served text file `%s'\n", ctx.Request)
			ok = true
			return
		case nr > 0:
			if nw, ew := ctx.conn.Write(buf[0:nr]); nw != nr {
				s.Logger.Printf("Error sending text file `%s': %s\n", ctx.Request, ew)
				err = ew
				return
			}
		}
	}
	return
}

type Server struct {
	listener net.Listener
	routes vector.Vector
	Logger *log.Logger
	Hostname string
	Port int
	Cwd string // Current working directory
}

type route struct {
	pattern string
	re *regexp.Regexp
	handler *reflect.FuncValue
}

var server = Server{
	Logger: log.New(os.Stdout, "", log.Ldate|log.Ltime),
}

func (s *Server) addRoute(pattern string, handler interface{}) {
	var re *regexp.Regexp
	var err os.Error
	if re, err = regexp.Compile(pattern); err != nil {
		s.Logger.Printf("Route failed to compile %q\n", pattern)
		return
	}
	if fv, ok := handler.(*reflect.FuncValue); ok {
		s.routes.Push(route{pattern, re, fv})
	} else {
		fv := reflect.NewValue(handler).(*reflect.FuncValue)
		s.routes.Push(route{pattern, re, fv})
	}
}

func (s *Server) handle(ctx *Context) (err os.Error) {
	defer ctx.conn.Close()
	linereader := line.NewReader(bufio.NewReader(ctx.conn), 512)
	read, _, err := linereader.ReadLine()
	if err != nil {
		s.Logger.Println("Malformed request from client")
		return
	}
	clientRequest := bytes.NewBuffer(read).String()
	s.Logger.Printf("REQUEST: %s\n", clientRequest)
	ctx.Request = "/"+strings.Trim(path.Clean(clientRequest), "/")
	absReqPath := path.Clean(fmt.Sprintf("%s%s", s.Cwd, ctx.Request))
		if !strings.HasPrefix(absReqPath, s.Cwd) {
		s.Logger.Printf("Requested file not in document root")
		return
	}
	var requestedFile *os.File
	if requestedFile, err = os.Open(absReqPath, 0, 0); err != nil {
		if patherr, ok := err.(*os.PathError); ok {
			switch true {
			case patherr.Error == os.ENOENT:
				ctx.Error(fmt.Sprintf("Resource `%s' not found", clientRequest))
				s.Logger.Printf("ERROR: Resource `%s' not found\n", ctx.Request)
				return
			case patherr == os.EPERM || patherr == os.EACCES:
				ctx.Error(fmt.Sprintf("Resource `%s' not found", clientRequest))
				s.Logger.Printf("ERROR: Access denied for file `%s'\n", ctx.Request)
				return
			default:
				s.Logger.Printf("ERROR: %s\n", err)
				return
			}
		} else {
			s.Logger.Printf("ERROR: %s\n", err)
			return
		}
	}
	stats, err := requestedFile.Stat()
	if err != nil {
		s.Logger.Printf("ERROR: Could not stat file `%s': %s\n", absReqPath, err)
		return
	}
	if stats.IsDirectory() {
		s.Directory(ctx, requestedFile)
	} else if stats.IsRegular() {
		s.Textfile(ctx, requestedFile)
	} else {
		ctx.Write(s.InfoLine("STUMPED"))
	}
	return
}

func (s *Server) init() {
	var err os.Error
	s.Cwd, err = os.Getwd();
	if err != nil {
		s.Logger.Printf("No access to the working directory: %s\n", err);
		os.Exit(1)
	}
}

func (s *Server) Run(hostname string, port int) {
	var err os.Error
	s.init()
	s.Hostname = hostname
	s.Port = port
	s.listener, err = net.Listen("tcp", fmt.Sprintf("%s:%d", s.Hostname, s.Port))
	if err != nil {
		panic(err)
	}
	s.Logger.Printf("listening on %s:%d...\n", s.Hostname, s.Port)
	for {
		if conn, err := s.listener.Accept(); err == nil {
			go s.handle(&Context{conn: conn})
		}
	}
}

func Run(hostname string, port int) {
	server.Run(hostname, port)
}

func main() {
	var defaulthost string
	var err os.Error
	if defaulthost, err = os.Hostname(); err != nil {
		fmt.Fprintln(os.Stderr, "could not determine hostname, defaulting to localhost")
		defaulthost = "localhost"
	}
	var hostname *string = flag.String("hostname", defaulthost, "hostname of the server")
	var port *int = flag.Int("port", 70, "port of the server")
	flag.Parse()
	Run(*hostname, *port)
}