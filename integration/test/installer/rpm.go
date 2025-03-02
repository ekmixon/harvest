package installer

import (
	"fmt"
	"github.com/Netapp/harvest-automation/test/utils"
	"log"
	"strings"
)

type RPM struct {
	path string
}

func (r *RPM) Init(path string) {
	r.path = path
}

func (r *RPM) Install() bool {
	harvestFile := "harvest.yml"
	rpmFileName := "harvest.rpm"
	utils.RemoveSafely(rpmFileName)
	err := utils.DownloadFile(rpmFileName, r.path)
	if err != nil {
		panic(err)
	}
	log.Println("Downloaded: " + r.path)
	Uninstall()
	harvestObj := new(Harvest)
	log.Println("Installing " + rpmFileName)
	installOutput := utils.Run("yum", "install", "-y", rpmFileName)
	log.Println(installOutput)
	log.Println("Stopping harvest before copying ONTAP certificates")
	harvestObj.Stop()
	log.Println("Copy certificates files into harvest directory")
	path := HarvestHome + "/certificates"
	if utils.FileExists(path) {
		err = utils.RemoveDir(path)
		utils.PanicIfNotNil(err)
	}
	utils.Run("mkdir", "-p", path)
	utils.Run("cp", "-R", utils.GetConfigDir()+"/certificates", HarvestHome)
	copyErr := utils.CopyFile(harvestFile, HarvestHome+"/harvest.yml")
	if copyErr != nil {
		return false
	} //use file directly from the repo
	harvestObj.Start()
	status := harvestObj.AllRunning()
	return status
}

func (r *RPM) Upgrade() bool {
	rpmFileName := "harvest.rpm"
	utils.RemoveSafely(rpmFileName)
	harvestObj := new(Harvest)
	if !harvestObj.AllRunning() {
		utils.PanicIfNotNil(fmt.Errorf("pollers are not in a running state before upgrade"))
	}
	versionCmd := []string{"-qa", "harvest"}
	previousVersion := strings.TrimSpace(utils.Run("rpm", versionCmd...))
	err := utils.DownloadFile(rpmFileName, r.path)
	utils.PanicIfNotNil(err)
	log.Println("Downloaded: " + r.path)
	log.Println("Updating " + rpmFileName)
	installOutput := utils.Run("yum", "upgrade", "-y", rpmFileName)
	log.Println(installOutput)
	installedVersion := strings.TrimSpace(utils.Run("rpm", versionCmd...))
	if previousVersion == installedVersion {
		utils.PanicIfNotNil(fmt.Errorf("upgrade is failed"))
	}
	status := harvestObj.AllRunning()
	return status
}
