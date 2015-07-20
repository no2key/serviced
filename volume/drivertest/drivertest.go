package drivertest

import (
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"syscall"

	"github.com/control-center/serviced/volume"
	. "gopkg.in/check.v1"
)

var (
	drv volume.Driver
)

type Driver struct {
	volume.Driver
	// Keep a reference to the root here just in case something below doesn't work
	root string
}

func newDriver(c *C, name, root string) *Driver {
	var err error
	if root == "" {
		root = c.MkDir()
	}
	d, err := volume.GetDriver(name, root)
	if err != nil {
		c.Logf("drivertest: %v\n", err)
		if err == volume.ErrDriverNotSupported {
			c.Skip("Driver not supported")
		}
		c.Fatal(err)
	}
	c.Assert(d.Root(), Equals, root)
	return &Driver{d, root}
}

func cleanup(c *C, d *Driver) {
	c.Check(d.Cleanup(), IsNil)
	os.RemoveAll(d.root)
}

func verifyFile(c *C, path string, mode os.FileMode, uid, gid uint32) {
	fi, err := os.Stat(path)
	c.Assert(err, IsNil)
	c.Assert(fi.Mode()&os.ModeType, Equals, mode&os.ModeType)
	c.Assert(fi.Mode()&os.ModePerm, Equals, mode&os.ModePerm)
	c.Assert(fi.Mode()&os.ModeSticky, Equals, mode&os.ModeSticky)
	c.Assert(fi.Mode()&os.ModeSetuid, Equals, mode&os.ModeSetuid)
	c.Assert(fi.Mode()&os.ModeSetgid, Equals, mode&os.ModeSetgid)
	if stat, ok := fi.Sys().(*syscall.Stat_t); ok {
		c.Assert(stat.Uid, Equals, uid)
		c.Assert(stat.Gid, Equals, gid)
	}
}

func arrayContains(array []string, element string) bool {
	for _, x := range array {
		if x == element {
			return true
		}
	}
	return false
}

// DriverTestCreateEmpty verifies that a driver can create a volume, and that
// is is empty (and owned by the current user) after creation.
func DriverTestCreateEmpty(c *C, drivername, root string) {
	driver := newDriver(c, drivername, root)
	defer cleanup(c, driver)

	volumeName := "empty"

	_, err := driver.Create(volumeName)
	c.Assert(err, IsNil)
	c.Assert(driver.Exists(volumeName), Equals, true)
	c.Assert(arrayContains(driver.List(), volumeName), Equals, true)
	vol, err := driver.Get(volumeName)
	c.Assert(err, IsNil)
	verifyFile(c, vol.Path(), 0755|os.ModeDir, uint32(os.Getuid()), uint32(os.Getgid()))
	fis, err := ioutil.ReadDir(vol.Path())
	c.Assert(err, IsNil)
	c.Assert(fis, HasLen, 0)

	driver.Release(volumeName)

	err = driver.Remove(volumeName)
	c.Assert(err, IsNil)
}

func createBase(c *C, driver *Driver, name string) volume.Volume {
	// We need to be able to set any perms
	oldmask := syscall.Umask(0)
	defer syscall.Umask(oldmask)

	_, err := driver.Create(name)
	c.Assert(err, IsNil)

	volume, err := driver.Get(name)
	c.Assert(err, IsNil)

	subdir := path.Join(volume.Path(), "a subdir")
	err = os.Mkdir(subdir, 0705|os.ModeSticky)
	c.Assert(err, IsNil)
	err = os.Chown(subdir, 1, 2)
	c.Assert(err, IsNil)

	file := path.Join(volume.Path(), "a file")
	err = ioutil.WriteFile(file, []byte("Some data"), 0222|os.ModeSetuid)
	c.Assert(err, IsNil)
	return volume
}

func writeExtra(c *C, driver *Driver, vol volume.Volume) {
	oldmask := syscall.Umask(0)
	defer syscall.Umask(oldmask)
	file := path.Join(vol.Path(), "differentfile")
	err := ioutil.WriteFile(file, []byte("more data"), 0222|os.ModeSetuid)
	c.Assert(err, IsNil)
}

func checkBase(c *C, driver *Driver, vol volume.Volume) {
	subdir := path.Join(vol.Path(), "a subdir")
	verifyFile(c, subdir, 0705|os.ModeDir|os.ModeSticky, 1, 2)

	file := path.Join(vol.Path(), "a file")
	verifyFile(c, file, 0222|os.ModeSetuid, 0, 0)
}

func verifyBase(c *C, driver *Driver, vol volume.Volume) {
	checkBase(c, driver, vol)
	fis, err := ioutil.ReadDir(vol.Path())
	c.Assert(err, IsNil)
	c.Assert(fis, HasLen, 2)
}

func verifyBaseWithExtra(c *C, driver *Driver, vol volume.Volume) {
	checkBase(c, driver, vol)

	file := path.Join(vol.Path(), "differentfile")
	verifyFile(c, file, 0222|os.ModeSetuid, 0, 0)

	fis, err := ioutil.ReadDir(vol.Path())
	c.Assert(err, IsNil)
	c.Assert(fis, HasLen, 3)
}

func DriverTestCreateBase(c *C, drivername, root string) {
	driver := newDriver(c, drivername, root)
	defer cleanup(c, driver)

	vol := createBase(c, driver, "Base")
	verifyBase(c, driver, vol)

	err := driver.Remove("Base")
	c.Assert(err, IsNil)
}

func DriverTestSnapshots(c *C, drivername, root string) {
	driver := newDriver(c, drivername, root)
	defer cleanup(c, driver)

	vol := createBase(c, driver, "Base")
	verifyBase(c, driver, vol)

	// Snapshot with the verified base
	err := vol.Snapshot("Snap")
	c.Assert(err, IsNil)

	snaps, err := vol.Snapshots()
	c.Assert(err, IsNil)
	fmt.Println(snaps)
	c.Assert(arrayContains(snaps, "Snap"), Equals, true)

	// Write another file
	writeExtra(c, driver, vol)

	// Re-snapshot with the extra file
	err = vol.Snapshot("Snap2")
	c.Assert(err, IsNil)

	// Rollback to the original snapshot and verify the base again
	err = vol.Rollback("Snap")
	c.Assert(err, IsNil)
	verifyBase(c, driver, vol)

	// Rollback to the new snapshot and verify the extra file
	err = vol.Rollback("Snap2")
	c.Assert(err, IsNil)
	verifyBaseWithExtra(c, driver, vol)

	// Make sure we still have all our snapshots
	snaps, err = vol.Snapshots()
	c.Assert(err, IsNil)
	c.Assert(arrayContains(snaps, "Snap"), Equals, true)
	c.Assert(arrayContains(snaps, "Snap2"), Equals, true)

	// Snapshot using an existing label and make sure it errors properly
	err = vol.Snapshot("Snap")
	c.Assert(err, ErrorMatches, volume.ErrSnapshotExists.Error())

	// Resnapshot using the raw label and make sure it is equivalent
	err = vol.Snapshot("Base_Snap")
	c.Assert(err, ErrorMatches, volume.ErrSnapshotExists.Error())

	err = driver.Remove("Base")
	c.Assert(err, IsNil)
}

func DriverTestExportImport(c *C, drivername, exportfs, importfs string) {
	backupdir := c.MkDir()
	outfile := filepath.Join(backupdir, "backup")

	exportDriver := newDriver(c, drivername, exportfs)
	defer cleanup(c, exportDriver)
	importDriver := newDriver(c, drivername, importfs)
	defer cleanup(c, importDriver)

	vol := createBase(c, exportDriver, "Base")
	writeExtra(c, exportDriver, vol)
	verifyBaseWithExtra(c, exportDriver, vol)
	c.Assert(vol.Snapshot("Backup"), IsNil)

	err := vol.Export("Backup", "", outfile)
	c.Assert(err, IsNil)

	vol2 := createBase(c, importDriver, "Base")
	err = vol2.Import("Base_Backup", outfile)
	c.Assert(err, IsNil)

	c.Assert(vol2.Rollback("Backup"), IsNil)
	verifyBaseWithExtra(c, importDriver, vol2)
}