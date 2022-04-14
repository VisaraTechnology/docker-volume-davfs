package main

import (
	"crypto/md5"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	// "strconv"
	"strings"
	"sync"

	"github.com/docker/go-plugins-helpers/volume"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const socketAddress = "/run/docker/plugins/davfs.sock"

type davfsVolume struct {
	URL      string
	Username string
	Password string
	Conf     string
	UID      string
	GID      string
	FileMode string
	DirMode  string
	Ro       bool
	Rw       bool
	Exec     bool
	Suid     bool
	Grpid    bool
	Netdev   bool

	Mountpoint  string
	connections int
}

type davfsDriver struct {
	sync.RWMutex

	root      string
	statePath string
	volumes   map[string]*davfsVolume
}

func newdavfsDriver(root string) (*davfsDriver, error) {
	log.Debug().Str("method", "new driver").Msg(root)

	d := &davfsDriver{
		root:      filepath.Join(root, "volumes"),
		statePath: filepath.Join(root, "state", "davfs-state.json"),
		volumes:   map[string]*davfsVolume{},
	}

	data, err := ioutil.ReadFile(d.statePath)
	if err != nil {
		if os.IsNotExist(err) {
			log.Debug().Str("statePath", d.statePath).Msg("no state found")
		} else {
			return nil, err
		}
	} else {
		if err := json.Unmarshal(data, &d.volumes); err != nil {
			return nil, err
		}
	}

	return d, nil
}

func (d *davfsDriver) saveState() {
	data, err := json.Marshal(d.volumes)
	if err != nil {
		log.Error().Str("statePath", d.statePath).Err(err).Msg("state convertion to json error")
		return
	}

	if err := ioutil.WriteFile(d.statePath, data, 0644); err != nil {
		log.Error().Str("statePath", d.statePath).Err(err).Msg("Writing state error")
	}
}

func (d *davfsDriver) Create(r *volume.CreateRequest) error {
	log.Debug().Str("method", "create").Interface("volume.CreateRequest", r).Send()

	d.Lock()
	defer d.Unlock()
	v := &davfsVolume{}

	for key, val := range r.Options {
		switch key {
		case "url":
			v.URL = val
		case "username":
			v.Username = val
		case "password":
			v.Password = val
		case "conf":
			v.Conf = val
		case "uid":
			v.UID = val
		case "gid":
			v.GID = val
		case "file_mode":
			v.FileMode = val
		case "dir_mode":
			v.DirMode = val
		case "ro":
			v.Ro = true
		case "rw":
			v.Rw = true
		case "exec":
			v.Exec = true
		case "suid":
			v.Suid = true
		case "grpid":
			v.Grpid = true
		case "_netdav":
			v.Netdev = true
		default:
			return logError("unknown option %q", val)
		}
	}

	if v.URL == "" {
		return logError("'url' option required")
	}
	_, err := url.Parse(v.URL)
	if err != nil {
		return logError("'url' option malformed")
	}
	v.Mountpoint = filepath.Join(d.root, fmt.Sprintf("%x", md5.Sum([]byte(v.URL))))

	d.volumes[r.Name] = v
	d.saveState()

	return nil
}

func (d *davfsDriver) Remove(r *volume.RemoveRequest) error {
	log.Debug().Str("method", "remove").Interface("volume.RemoveRequest", r).Send()

	d.Lock()
	defer d.Unlock()

	v, ok := d.volumes[r.Name]
	if !ok {
		return logError("volume %s not found", r.Name)
	}

	if v.connections != 0 {
		return logError("volume %s is currently used by a container", r.Name)
	}
	if err := os.RemoveAll(v.Mountpoint); err != nil {
		return logError(err.Error())
	}
	delete(d.volumes, r.Name)
	d.saveState()
	return nil
}

func (d *davfsDriver) Path(r *volume.PathRequest) (*volume.PathResponse, error) {
	log.Debug().Str("method", "path").Interface("volume.PathRequest", r).Send()

	d.RLock()
	defer d.RUnlock()

	v, ok := d.volumes[r.Name]
	if !ok {
		return &volume.PathResponse{}, logError("volume %s not found", r.Name)
	}

	return &volume.PathResponse{Mountpoint: v.Mountpoint}, nil
}

func (d *davfsDriver) Mount(r *volume.MountRequest) (*volume.MountResponse, error) {
	log.Debug().Str("method", "mount").Interface("volume.MountRequest", r).Send()

	d.Lock()
	defer d.Unlock()

	v, ok := d.volumes[r.Name]
	if !ok {
		return &volume.MountResponse{}, logError("volume %s not found", r.Name)
	}

	if v.connections == 0 {
		fi, err := os.Lstat(v.Mountpoint)
		if os.IsNotExist(err) {
			if err := os.MkdirAll(v.Mountpoint, 0755); err != nil {
				return &volume.MountResponse{}, logError(err.Error())
			}
		} else if err != nil {
			return &volume.MountResponse{}, logError(err.Error())
		}

		if fi != nil && !fi.IsDir() {
			return &volume.MountResponse{}, logError("%v already exist and it's not a directory", v.Mountpoint)
		}

		if buffer, err := d.mountVolume(v); err != nil {
			log.Info().Str("output", string(buffer)).Msg("Try to connect to webdav")
			return &volume.MountResponse{}, logError(err.Error())
		}
	}
	v.connections++

	return &volume.MountResponse{Mountpoint: v.Mountpoint}, nil
}

func (d *davfsDriver) Unmount(r *volume.UnmountRequest) error {
	log.Debug().Str("method", "unmount").Interface("volume.UnmountRequest", r).Send()

	d.Lock()
	defer d.Unlock()
	v, ok := d.volumes[r.Name]
	if !ok {
		return logError("volume %s not found", r.Name)
	}

	v.connections--

	if v.connections <= 0 {
		if err := d.unmountVolume(v.Mountpoint); err != nil {
			return logError(err.Error())
		}
		v.connections = 0
	}

	return nil
}

func (d *davfsDriver) Get(r *volume.GetRequest) (*volume.GetResponse, error) {
	log.Debug().Str("method", "get").Interface("volume.GetRequest", r).Send()

	d.Lock()
	defer d.Unlock()

	v, ok := d.volumes[r.Name]
	if !ok {
		return &volume.GetResponse{}, logError("volume %s not found", r.Name)
	}

	return &volume.GetResponse{Volume: &volume.Volume{Name: r.Name, Mountpoint: v.Mountpoint}}, nil
}

func (d *davfsDriver) List() (*volume.ListResponse, error) {
	log.Debug().Str("method", "list").Send()

	d.Lock()
	defer d.Unlock()

	var vols []*volume.Volume
	for name, v := range d.volumes {
		vols = append(vols, &volume.Volume{Name: name, Mountpoint: v.Mountpoint})
	}
	return &volume.ListResponse{Volumes: vols}, nil
}

func (d *davfsDriver) Capabilities() *volume.CapabilitiesResponse {
	log.Debug().Str("method", "capabilities").Send()

	return &volume.CapabilitiesResponse{Capabilities: volume.Capability{Scope: "local"}}
}

func (d *davfsDriver) mountVolume(v *davfsVolume) ([]byte, error) {
	log.Debug().Str("method", "mountVolume").Send()

	u, err := url.Parse(v.URL)
	if err != nil {
		log.Fatal().Err(err)
	}

	log.Debug().Str("method", "mountVolume").Str("url", u.String())

	completeUrl := fmt.Sprintf("%s://%s%s", u.Scheme, u.Host, u.Path)
	cmd := exec.Command("mount.davfs", completeUrl, v.Mountpoint)

	if v.Conf != "" {
		cmd.Args = append(cmd.Args, "-o", fmt.Sprintf("conf=%s", v.Conf))
	}
	if v.UID != "" {
		exec.Command("adduser", "-S", "-u", v.UID, v.UID).Run()
		cmd.Args = append(cmd.Args, "-o", fmt.Sprintf("uid=%s", v.UID))
	}
	if v.GID != "" {
		exec.Command("addgroup", "-S", "-g", v.GID, v.GID).Run()
		cmd.Args = append(cmd.Args, "-o", fmt.Sprintf("gid=%s", v.GID))
	}
	if v.FileMode != "" {
		cmd.Args = append(cmd.Args, "-o", fmt.Sprintf("file_mode=%s", v.FileMode))
	}
	if v.DirMode != "" {
		cmd.Args = append(cmd.Args, "-o", fmt.Sprintf("dir_mode=%s", v.DirMode))
	}
	if v.Ro {
		cmd.Args = append(cmd.Args, "-o", "ro")
	}
	if v.Rw {
		cmd.Args = append(cmd.Args, "-o", "rw")
	}
	if v.Exec {
		cmd.Args = append(cmd.Args, "-o", "exec")
	}
	if v.Suid {
		cmd.Args = append(cmd.Args, "-o", "suid")
	}
	if v.Grpid {
		cmd.Args = append(cmd.Args, "-o", "grpid")
	}
	if v.Netdev {
		cmd.Args = append(cmd.Args, "-o", "_netdev")
	}

	var username string = ""
	var password string = ""

	if u.User != nil {
		username = u.User.Username()
		passwordTmp, isSetted := u.User.Password()

		if isSetted {
			password = passwordTmp
		} else if v.Username != "" {
			password = v.Password
		}
	} else if v.Username != "" {
		username = v.Username
		password = v.Password
	}

	if username != "" {
		// cmd.Args = append(cmd.Args, "-o", fmt.Sprintf("username=%s", username))
		cmd.Stdin = strings.NewReader(fmt.Sprintf("%s\n%s\n", username, password))
	}

	log.Debug().Strs("Args", cmd.Args).Send()
	return cmd.CombinedOutput()
}

func (d *davfsDriver) unmountVolume(target string) error {
	cmd := fmt.Sprintf("umount %s", target)
	log.Debug().Msg(cmd)
	return exec.Command("sh", "-c", cmd).Run()
}

func logError(format string, args ...interface{}) error {
	log.Error().Msgf(format, args...)
	return fmt.Errorf(format, args)
}

func main() {
	logFile, err := os.OpenFile("/docker-volume-davfs.log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0777)
	if err != nil {
		panic(err)
	}
	defer logFile.Close()

	log.Logger = log.Output(zerolog.ConsoleWriter{Out: logFile})

	zerolog.SetGlobalLevel(zerolog.DebugLevel)

	/*debug := os.Getenv("DEBUG")
	if ok, _ := strconv.ParseBool(debug); !ok {
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}*/

	// make sure "/etc/davfs2/secrets" is owned by root
	err = os.Chown("/etc/davfs2/secrets", 0, 0)
	if err != nil {
		log.Fatal().Err(err)
	}

	d, err := newdavfsDriver("/mnt")
	if err != nil {
		log.Fatal().Err(err)
	}
	h := volume.NewHandler(d)

	log.Info().Msgf("Listening on %s", socketAddress)
	log.Error().Err(h.ServeUnix(socketAddress, 0))
}
