

package server


import "bufio"
import "fmt"
import "io"
import "os"
import "syscall"

import "vgl/transcript"


type Server struct {
	configuration *ServerConfiguration
	state serverState
	isolates chan func () ()
	process *os.Process
	transcript transcript.Transcript
}

type serverState int
const (
	invalidServerStateMin serverState = iota
	serverCreated
	serverRunning
	serverTerminated
	serverFailed
	invalidServerStateMax
)

const isolatesBufferSize = 16


func Create (_configuration *ServerConfiguration) (*Server, error) {
	
	_server := & Server {
		configuration : _configuration,
		state : serverCreated,
		isolates : make (chan func () (), isolatesBufferSize),
		process : nil,
		transcript : nil,
	}
	
	_server.transcript = transcript.NewTranscript (_server, packageTranscript)
	_server.transcript.TraceDebugging ("created mysql server controller.")
	
	go _server.executeLoop ()
	
	return _server, nil
}


func (_server *Server) Start (_bootstrap bool) (error) {
	_completion := make (chan error, 1)
	defer close (_completion)
	_server.isolates <- func () () {
		if _bootstrap {
			if _error := _server.handleBootstrap (); _error != nil {
				_completion <- _error
				return
			}
		}
		if _error := _server.handleStart (); _error != nil {
			_completion <- _error
			return
		}
		_completion <- nil
	}
	return <- _completion
}


func (_server *Server) Terminate () (error) {
	_completion := make (chan error, 1)
	defer close (_completion)
	_server.isolates <- func () () {
		if _server.state == serverTerminated {
			_completion <- fmt.Errorf ("illegal-state")
			return
		} else if _server.state != serverRunning {
			_completion <- fmt.Errorf ("illegal-state")
			return
		}
		if _error := _server.handleStop (); _error != nil {
			_completion <- _error
			return
		}
		_completion <- nil
	}
	return <- _completion
}


func (_server *Server) handleBootstrap () (error) {
	
	if _server.state != serverCreated {
		return fmt.Errorf ("illegal-state")
	}
	
	_server.transcript.TraceInformation ("bootstrapping...")
	
	_markerPath := _server.configuration.GenericConfiguration.DatabasesPath + "/.bootstrapp.marker"
	
	var _markerFile *os.File
	defer func () () {
		if _markerFile == nil {
			return
		}
		if _, _error := _markerFile.Write ([]byte ("failed!\n")); _error != nil {
			panic (_error)
		}
		if _error := _markerFile.Close (); _error != nil {
			panic (_error)
		}
	} ()
	if _markerFile_1, _error := os.OpenFile (_markerPath, os.O_WRONLY | os.O_CREATE | os.O_EXCL, 0444); _error != nil {
		return _error
	} else {
		_markerFile = _markerFile_1
	}
	if _, _error := _markerFile.Write ([]byte ("pending...\n")); _error != nil {
		panic (_error)
	}
	
	_executable, _arguments, _environment, _directory := prepareBootstrapExecution (_server.configuration)
	_console := _server.prepareConsole ()
	
	var _scriptFile *os.File
	defer func () () {
		if _scriptFile == nil {
			return
		}
		if _error := _scriptFile.Close (); _error != nil {
			panic (_error)
		}
	} ()
	if _scriptFile_1, _error := prepareBootstrapScript (_server.configuration); _error != nil {
		return _error
	} else {
		_scriptFile = _scriptFile_1
	}
	
	_attributes := & os.ProcAttr {
			Env : _environment,
			Dir : _directory,
			Files : []*os.File {
					_scriptFile,
					nil,
					_console,
			},
			Sys : & syscall.SysProcAttr {
					Pdeathsig : syscall.SIGTERM,
			},
	}
	
	_server.transcript.TraceDebugging ("process arguments: `%v`", _arguments)
	
	var _process *os.Process
	if _process_1, _error := os.StartProcess (_executable, _arguments, _attributes); _error != nil {
		return _error
	} else {
		_process = _process_1
	}
	
	if _error := _scriptFile.Close (); _error != nil {
		panic (_error)
	}
	_scriptFile = nil
	
	if _state, _error := _process.Wait (); _error != nil {
		return _error
	} else if !_state.Success () {
		return fmt.Errorf ("bootstrap process failed")
	}
	
	if _error := _markerFile.Truncate (0); _error != nil {
		panic (_error)
	}
	if _error := _markerFile.Close (); _error != nil {
		panic (_error)
	}
	_markerFile = nil
	
	_server.transcript.TraceInformation ("bootstrapped.")
	return nil
}


func (_server *Server) handleStart () (error) {
	
	if _server.state != serverCreated {
		return fmt.Errorf ("illegal-state")
	}
	
	_server.transcript.TraceInformation ("starting...")
	
	_executable, _arguments, _environment, _directory := prepareServerExecution (_server.configuration)
	_console := _server.prepareConsole ()
	
	_attributes := & os.ProcAttr {
			Env : _environment,
			Dir : _directory,
			Files : []*os.File {
					nil,
					nil,
					_console,
			},
			Sys : & syscall.SysProcAttr {
					Pdeathsig : syscall.SIGTERM,
			},
	}
	
	_server.transcript.TraceDebugging ("process arguments: `%v`", _arguments)
	
	var _process *os.Process
	if _process_1, _error := os.StartProcess (_executable, _arguments, _attributes); _error != nil {
		return _error
	} else {
		_process = _process_1
	}
	
	_server.process = _process
	_server.state = serverRunning
	
	_server.transcript.TraceInformation ("started.")
	return nil
}


func (_server *Server) handleStop () (error) {
	
	_server.transcript.TraceInformation ("stopping...")
	
	if _server.state != serverRunning {
		return fmt.Errorf ("illegal-state")
	}
	
	if _error := _server.process.Signal (syscall.SIGTERM); _error != nil {
		return _error
	}
	
	if _, _error := _server.process.Wait (); _error != nil {
		return _error
	}
	
	_server.state = serverTerminated
	
	_server.transcript.TraceInformation ("stopped...")
	return nil
}


func (_server *Server) executeLoop () () {
	for {
		_isolate, _ok := <- _server.isolates
		if !_ok {
			_server.isolates = nil
			break
		}
		_isolate ()
	}
	if _server.isolates != nil {
		close (_server.isolates)
		_server.isolates = nil
	}
}


func prepareBootstrapScript (_configuration *ServerConfiguration) (*os.File, error) {
	
	_scriptContents := make ([][]byte, 0, 16)
	
	_scriptContents = append (_scriptContents, []byte ("CREATE DATABASE mysql;\n"))
	_scriptContents = append (_scriptContents, []byte ("USE mysql;"))
	
	for _, _scriptPath := range _configuration.SqlInitializationScriptPaths {
		// FIXME: Prevent file descriptor leak on error!
		if _scriptFile, _error := os.Open (_scriptPath); _error != nil {
			return nil, _error
		} else if _scriptStat, _error := _scriptFile.Stat (); _error != nil {
			return nil, _error
		} else {
			// FIXME: Enforce a "sane" file size!
			_scriptSize := int (_scriptStat.Size ())
			_scriptContent := make ([]byte, _scriptSize)
			if _read, _error := io.ReadFull (_scriptFile, _scriptContent); _error != nil {
				return nil, _error
			} else if _read != _scriptSize {
				return nil, fmt.Errorf ("script read unexpected data amount")
			}
			_scriptContents = append (_scriptContents, _scriptContent)
		}
	}
	
	_scriptContents = append (_scriptContents, []byte (
			fmt.Sprintf (
					`UPDATE mysql.user SET password = PASSWORD ('%s') WHERE user = 'root';`, _configuration.SqlAdministratorPassword)))
	
	var _reader, _writer *os.File
	if _reader_1, _writer_1, _error := os.Pipe (); _error != nil {
		return nil, _error
	} else {
		_reader = _reader_1
		_writer = _writer_1
	}
	
	go func () () {
		for _, _scriptContent := range _scriptContents {
			if _, _error := _writer.Write (_scriptContent); _error != nil {
				panic (_error)
			}
		}
		if _error := _writer.Close (); _error != nil {
			panic (_error)
		}
	} ()
	
	return _reader, nil
}


func (_server *Server) prepareConsole () (*os.File) {
	
	var _reader, _writer *os.File
	if _reader_1, _writer_1, _error := os.Pipe (); _error != nil {
		panic (_error)
	} else {
		_reader = _reader_1
		_writer = _writer_1
	}
	
	go func () () {
		// FIXME: Handle errors!
		_scanner := bufio.NewScanner (_reader)
		for _scanner.Scan () {
			_server.transcript.TraceInformation (">>  %s", _scanner.Text ())
		}
		_reader.Close ()
	} ()
	
	return _writer
}


func prepareServerExecution (_configuration *ServerConfiguration) (string, []string, []string, string) {
	
	_executable, _arguments, _environment, _directory := prepareGenericExecution (_configuration)
	
	pushStringf (&_arguments, "--bind-address=%s", _configuration.SqlEndpointIp.String ())
	pushStringf (&_arguments, "--port=%d", _configuration.SqlEndpointPort)
	
	pushStrings (&_arguments, "--extra-port=0")
	pushStrings (&_arguments, "--skip-ssl", "--skip-name-resolve", "--skip-host-cache")
	
	return _executable, _arguments, _environment, _directory
}

func prepareBootstrapExecution (_configuration *ServerConfiguration) (string, []string, []string, string) {
	
	_executable, _arguments, _environment, _directory := prepareGenericExecution (_configuration)
	
	pushStrings (&_arguments, "--bootstrap")
	pushStrings (&_arguments, "--skip-grant")
	pushStrings (&_arguments, "--skip-networking")
	pushStrings (&_arguments, "--one-thread")
	
	return _executable, _arguments, _environment, _directory
}

func prepareGenericExecution (_configuration *ServerConfiguration) (string, []string, []string, string) {
	
	_executable := _configuration.GenericConfiguration.ExecutablePath
	_directory := _configuration.GenericConfiguration.TemporaryPath
	_arguments := make ([]string, 0, 128)
	_environment := make ([]string, 0, 128)
	
	pushStrings (&_arguments, _executable)
	
	pushStrings (&_arguments, "--no-defaults")
	
	pushStringf (&_arguments, "--basedir=%s", _configuration.GenericConfiguration.PackageBasePath)
	pushStringf (&_arguments, "--character-sets-dir=%s", _configuration.GenericConfiguration.CharsetsPath)
	pushStringf (&_arguments, "--plugin-dir=%s", _configuration.GenericConfiguration.PluginsPath)
	pushStringf (&_arguments, "--datadir=%s", _configuration.GenericConfiguration.DatabasesPath)
	pushStringf (&_arguments, "--tmpdir=%s", _configuration.GenericConfiguration.TemporaryPath)
	pushStringf (&_arguments, "--socket=%s", _configuration.GenericConfiguration.SocketPath)
	pushStringf (&_arguments, "--pid-file=%s", _configuration.GenericConfiguration.PidPath)
	
	pushStrings (&_arguments, "--memlock")
	pushStrings (&_arguments, "--console", "--log-warnings")
	
	return _executable, _arguments, _environment, _directory
}

func pushStrings (_collection *[]string, _values ... string) () {
	*_collection = append (*_collection, _values ...)
}

func pushStringf (_collection *[]string, _format string, _parts ... interface{}) () {
	pushStrings (_collection, fmt.Sprintf (_format, _parts ...))
}
