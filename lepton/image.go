package lepton

import (
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"

	"github.com/go-errors/errors"
)

// BuildImage builds a unikernel image for user
// supplied ELF binary.
func BuildImage(userImage string, c Config) error {
	var err error
	m, err := BuildManifest(userImage, &c)
	if err != nil {
		return errors.Wrap(err, 1)
	}
	if err = buildImage(userImage, &c, m); err != nil {
		return errors.Wrap(err, 1)
	}
	return nil
}

func BuildImageFromPackage(program string, packagepath string, c Config) error {
	m, err := BuildPackageManifest(packagepath, &c, program)
	if err != nil {
		return errors.Wrap(err, 1)
	}
	if err := buildImage(program, &c, m); err != nil {
		return errors.Wrap(err, 1)
	}
	return nil
}

func createFile(filepath string) (*os.File, error) {
	path := path.Dir(filepath)
	var _, err = os.Stat(path)
	if os.IsNotExist(err) {
		os.MkdirAll(path, os.ModePerm)
	}
	fd, err := os.Create(filepath)
	if err != nil {
		return nil, errors.Wrap(err, 1)
	}
	return fd, nil
}

// add /etc/resolv.conf
func addDNSConfig(m *Manifest, c *Config) {
	data := []byte("nameserver ")
	data = append(data, []byte(c.NameServer)...)
	temp := path.Join(os.TempDir(), "resolv")
	err := ioutil.WriteFile(temp, data, 0644)
	if err != nil {
		panic(err)
	}
	//nameserver 127.0.1.1
	m.AddFile("/etc/resolv.conf", temp)
}

///proc/sys/kernel/hostname
func addHostName(m *Manifest, c *Config) {
	// uniboot is hardcoded in nanos virtio
	// may be better to handle 'proc/sys/kernel/hostname' open
	// in nanos as a spcial file like other device files
	data := []byte("uniboot")
	temp := path.Join(os.TempDir(), "hostname")
	err := ioutil.WriteFile(temp, data, 0644)
	if err != nil {
		panic(err)
	}
	//nameserver 127.0.1.1
	m.AddFile("/proc/sys/kernel/hostname", temp)
}

func BuildPackageManifest(packagepath string, c *Config, userImage string) (*Manifest, error) {
	initDefaultImages(c)
	m := NewManifest()
	addFromConfig(userImage, m, c)

	// Add files from package

	return m, nil
}

func addFromConfig(userImage string, m *Manifest, c *Config) {
	m.AddUserProgram(userImage)
	m.AddKernal(c.Kernel)
	addDNSConfig(m, c)
	addHostName(m, c)
	for _, f := range c.Files {
		m.AddFile(f, f)
	}
	for k, v := range c.MapDirs {
		addMappedFiles(k, v, m)
	}
	for _, d := range c.Dirs {
		m.AddDirectory(d)
	}
	for _, a := range c.Args {
		m.AddArgument(a)
	}
	for _, dbg := range c.Debugflags {
		m.AddDebugFlag(dbg, 't')
	}
	for k, v := range c.Env {
		m.AddEnvironmentVariable(k, v)
	}
}

func BuildManifest(userImage string, c *Config) (*Manifest, error) {
	initDefaultImages(c)
	m := NewManifest()
	addFromConfig(userImage, m, c)
	// run ldd and capture dependencies
	fmt.Println("Finding dependent shared libs")
	deps, err := getSharedLibs(userImage)
	if err != nil {
		return nil, errors.Wrap(err, 1)
	}
	for _, libpath := range deps {
		m.AddLibrary(libpath)
	}
	return m, nil
}

func initDefaultImages(c *Config) {
	if c.Boot == "" {
		c.Boot = BootImg
	}
	if c.Kernel == "" {
		c.Kernel = KernelImg
	}
	if c.DiskImage == "" {
		c.DiskImage = FinalImg
	}
	if c.Mkfs == "" {
		c.Mkfs = Mkfs
	}
	if c.NameServer == "" {
		// google dns server
		c.NameServer = "8.8.8.8"
	}
}

func addMappedFiles(src string, dest string, m *Manifest) {
	dir, pattern := filepath.Split(src)
	filepath.Walk(dir, func(hostpath string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		_, filename := filepath.Split(hostpath)
		matched, _ := filepath.Match(pattern, filename)
		if matched {
			vmpath := filepath.Join(dest, filename)
			m.AddFile(vmpath, hostpath)
		}
		return nil
	})
}

func buildImage(userImage string, c *Config, m *Manifest) error {
	//  prepare manifest file
	var elfmanifest string

	elfmanifest = m.String()
	// invoke mkfs to create the filesystem ie kernel + elf image
	createFile(mergedImg)
	mkfs := exec.Command(c.Mkfs, mergedImg)
	stdin, err := mkfs.StdinPipe()
	if err != nil {
		return errors.Wrap(err, 1)
	}
	_, err = io.WriteString(stdin, elfmanifest)
	if err != nil {
		return errors.Wrap(err, 1)
	}
	out, err := mkfs.CombinedOutput()
	if err != nil {
		log.Println(string(out))
		return errors.Wrap(err, 1)
	}

	// produce final image, boot + kernel + elf
	fd, err := createFile(c.DiskImage)
	defer fd.Close()
	if err != nil {
		return errors.Wrap(err, 1)
	}
	catcmd := exec.Command("cat", c.Boot, mergedImg)
	catcmd.Stdout = fd
	err = catcmd.Start()
	if err != nil {
		return errors.Wrap(err, 1)
	}
	catcmd.Wait()
	return nil
}

type dummy struct {
	total uint64
}

func (bc dummy) Write(p []byte) (int, error) {
	return len(p), nil
}

func DownloadBootImages() error {
	return DownloadImages(dummy{}, ReleaseBaseUrl)
}

// DownloadImages downloads latest kernel images.
func DownloadImages(w io.Writer, baseUrl string) error {
	var err error
	if _, err := os.Stat(".staging"); os.IsNotExist(err) {
		os.MkdirAll(".staging", 0755)
	}

	if _, err = os.Stat(Mkfs); os.IsNotExist(err) {
		if err = downloadFile(Mkfs, fmt.Sprintf(baseUrl, path.Join(runtime.GOOS, "mkfs")), w); err != nil {
			return errors.Wrap(err, 1)
		}
	}

	// make mkfs executable
	err = os.Chmod(Mkfs, 0775)
	if err != nil {
		return errors.Wrap(err, 1)
	}

	if _, err = os.Stat(BootImg); os.IsNotExist(err) {
		if err = downloadFile(BootImg, fmt.Sprintf(baseUrl, "boot.img"), w); err != nil {
			return errors.Wrap(err, 1)
		}
	}

	if _, err = os.Stat(KernelImg); os.IsNotExist(err) {
		if err = downloadFile(KernelImg, fmt.Sprintf(baseUrl, "stage3.img"), w); err != nil {
			return errors.Wrap(err, 1)
		}
	}
	return nil
}

func DownloadFile(filepath string, url string) error {
	return downloadFile(filepath, url, dummy{})
}

func downloadFile(filepath string, url string, w io.Writer) error {
	// download to a temp file and later rename it
	out, err := os.Create(filepath + ".tmp")
	if err != nil {
		return errors.Wrap(err, 1)
	}
	defer out.Close()
	resp, err := http.Get(url)
	if err != nil {
		return errors.Wrap(err, 1)
	}
	defer resp.Body.Close()
	// progress reporter.
	_, err = io.Copy(out, io.TeeReader(resp.Body, w))
	if err != nil {
		return errors.Wrap(err, 1)
	}
	err = os.Rename(filepath+".tmp", filepath)
	if err != nil {
		return errors.Wrap(err, 1)
	}
	return nil
}
