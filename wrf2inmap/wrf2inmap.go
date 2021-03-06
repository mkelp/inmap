/*
Copyright © 2013 the InMAP authors.
This file is part of InMAP.

InMAP is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

InMAP is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.

You should have received a copy of the GNU General Public License
along with InMAP.  If not, see <http://www.gnu.org/licenses/>.
*/

// Package wrf2inmap processes WRFChem output files into InMAP input files.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"math"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"bitbucket.org/ctessum/cdf"
	"bitbucket.org/ctessum/sparse"
	"github.com/ctessum/atmos/acm2"
	"github.com/ctessum/atmos/emep"
	"github.com/spatialmodel/inmap"
	//"github.com/ctessum/atmos/gocart"
	"github.com/ctessum/atmos/seinfeld"
	"github.com/ctessum/atmos/wesely1989"
)

// ConfigInfo holds the configuration information for the program run.
type ConfigInfo struct {
	Wrfout           string  // Location of WRF output files. [DATE] is a wild card for the simulation date.
	OutputDir        string  // Directory to put the output files in
	OutputFilePrefix string  // name for output files
	StartDate        string  // Format = "YYYYMMDD"
	EndDate          string  // Format = "YYYYMMDD"
	CtmGridXo        float64 // lower left of Chemical Transport Model (CTM) grid, x
	CtmGridYo        float64 // lower left of grid, y
	CtmGridDx        float64 // m
	CtmGridDy        float64 // m
	CtmGridNx        int
	CtmGridNy        int
	GridProj         string // projection info for CTM grid; Proj4 format
}

// WRF variables currently used:
/* hc5,hc8,olt,oli,tol,xyl,csl,cvasoa1,cvasoa2,cvasoa3,cvasoa4,iso,api,sesq,lim,
cvbsoa1,cvbsoa2,cvbsoa3,cvbsoa4,asoa1i,asoa1j,asoa2i,asoa2j,asoa3i,asoa3j,asoa4i,
asoa4j,bsoa1i,bsoa1j,bsoa2i,bsoa2j,bsoa3i,bsoa3j,bsoa4i,bsoa4j,no,no2,no3ai,no3aj,
so2,sulf,so4ai,so4aj,nh3,nh4ai,nh4aj,PM2_5_DRY,U,V,W,PBLH,PH,PHB,HFX,UST,PBLH,T,
PB,P,ho,h2o2,LU_INDEX,QRAIN,CLDFRA,QCLOUD,ALT,SWDOWN,GLW */

const (
	wrfFormat    = "2006-01-02_15_04_05"
	inDateFormat = "20060102"
	tolerance    = 1.e-10 // tolerance for comparing floats
)

// physical constants
const (
	MWa      = 28.97   // g/mol, molar mass of air
	mwN      = 14.0067 // g/mol, molar mass of nitrogen
	mwS      = 32.0655 // g/mol, molar mass of sulfur
	mwNH4    = 18.03851
	mwSO4    = 96.0632
	mwNO3    = 62.00501
	g        = 9.80665 // m/s2
	κ        = 0.41    // Von Kármán constant
	atmPerPa = 9.86923267e-6
)

var (
	start      time.Time
	end        time.Time
	current    time.Time
	numTsteps  float64
	configFile = flag.String("config", "none", "Path to configuration file")
	config     = new(ConfigInfo)
)

var (
	// RACM VOC species and molecular weights (g/mol);
	// Only includes anthropogenic precursors to SOA from
	// anthropogenic (aSOA) and biogenic (bSOA) sources as
	// in Ahmadov et al. (2012)
	// Assume condensable vapor from SOA has molar mass of 70
	aVOC = map[string]float64{"hc5": 72, "hc8": 114,
		"olt": 42, "oli": 68, "tol": 92, "xyl": 106, "csl": 108,
		"cvasoa1": 70, "cvasoa2": 70, "cvasoa3": 70, "cvasoa4": 70}
	bVOC = map[string]float64{"iso": 68, "api": 136, "sesq": 84.2,
		"lim": 136, "cvbsoa1": 70, "cvbsoa2": 70,
		"cvbsoa3": 70, "cvbsoa4": 70}
	// VBS SOA species (anthropogenic only)
	aSOA = map[string]float64{"asoa1i": 1, "asoa1j": 1, "asoa2i": 1,
		"asoa2j": 1, "asoa3i": 1, "asoa3j": 1, "asoa4i": 1, "asoa4j": 1}
	// VBS SOA species (biogenic only)
	bSOA = map[string]float64{"bsoa1i": 1, "bsoa1j": 1, "bsoa2i": 1,
		"bsoa2j": 1, "bsoa3i": 1, "bsoa3j": 1, "bsoa4i": 1, "bsoa4j": 1}
	// NOx is RACM NOx species and molecular weights, multiplied by their
	// nitrogen fractions
	NOx = map[string]float64{"no": 30 / 30 * mwN, "no2": 46 / 46 * mwN}
	// NO is the mass of N in NO
	NO = map[string]float64{"no": 1.}
	// NO2 is the mass of N in  NO2
	NO2 = map[string]float64{"no2": 1.}
	// pNO is the MADE particulate NO species, nitrogen fraction
	pNO = map[string]float64{"no3ai": mwN / mwNO3, "no3aj": mwN / mwNO3}
	// SOx is the RACM SOx species and molecular weights
	SOx = map[string]float64{"so2": 64 / 64 * mwS, "sulf": 98 / 98 * mwS}
	// pS is the MADE particulate Sulfur species; sulfur fraction
	pS = map[string]float64{"so4ai": mwS / mwSO4, "so4aj": mwS / mwSO4}
	// NH3 is ammonia
	NH3 = map[string]float64{"nh3": 17.03056 * 17.03056 / mwN}
	// pNH is the MADE particulate ammonia species, nitrogen fraction
	pNH = map[string]float64{"nh4ai": mwN / mwNH4, "nh4aj": mwN / mwNH4}

	totalPM25 = map[string]float64{"PM2_5_DRY": 1.}
)

func main() {
	var err error

	flag.Parse()
	if *configFile == "" {
		log.Println("Need to specify configuration file as in " +
			"`wrf2inmap -config=configFile.json`")
		os.Exit(1)
	}
	ReadConfigFile(*configFile)

	start, err = time.Parse(inDateFormat, config.StartDate)
	if err != nil {
		panic(err)
	}
	end, err = time.Parse(inDateFormat, config.EndDate)
	if err != nil {
		panic(err)
	}
	end = end.AddDate(0, 0, 1) // add 1 day to the end
	numTsteps = math.NaN()     // We will set the correct number later.

	go func() {
		http.ListenAndServe("localhost:6060", nil)
	}()

	pblhChan := make(chan *sparse.DenseArray)
	go average(pblhChan)
	phChan := make(chan *sparse.DenseArray)
	go average(phChan)
	phbChan := make(chan *sparse.DenseArray)
	go average(phbChan)
	uAvgChan := make(chan *sparse.DenseArray)
	vAvgChan := make(chan *sparse.DenseArray)
	wAvgChan := make(chan *sparse.DenseArray)
	go windSpeed(uAvgChan, vAvgChan, wAvgChan)

	iterateTimeSteps("Reading data--pass 1: ",
		readSingleVar("U", uAvgChan),
		readSingleVar("V", vAvgChan),
		readSingleVar("W", wAvgChan),
		readSingleVar("PBLH", pblhChan),
		readSingleVar("PH", phChan), readSingleVar("PHB", phbChan))

	pblhChan <- nil
	phChan <- nil
	phbChan <- nil
	uAvgChan <- nil
	vAvgChan <- nil
	wAvgChan <- nil
	pblh := <-pblhChan
	ph := <-phChan
	phb := <-phbChan
	windSpeed := <-uAvgChan
	windSpeedInverse := <-uAvgChan
	windSpeedMinusThird := <-uAvgChan
	windSpeedMinusOnePointFour := <-uAvgChan
	uAvg := <-uAvgChan
	vAvg := <-vAvgChan
	wAvg := <-wAvgChan

	layerHeights, Dz := calcLayerHeights(ph, phb)

	// calculate deviation from average wind speed.
	// Only calculate horizontal deviations.
	deviationChanU := make(chan *sparse.DenseArray)
	deviationChanV := make(chan *sparse.DenseArray)
	go windDeviation(uAvg, deviationChanU, 2)
	go windDeviation(vAvg, deviationChanV, 1)

	// calculate gas/particle partitioning
	aVOCchan := make(chan *sparse.DenseArray)
	aSOAchan := make(chan *sparse.DenseArray)
	go calcPartitioning(aVOCchan, aSOAchan)
	bVOCchan := make(chan *sparse.DenseArray)
	bSOAchan := make(chan *sparse.DenseArray)
	go calcPartitioning(bVOCchan, bSOAchan)
	NOxchan := make(chan *sparse.DenseArray)
	pNOchan := make(chan *sparse.DenseArray)
	go calcPartitioning(NOxchan, pNOchan)
	SOxchan := make(chan *sparse.DenseArray)
	pSchan := make(chan *sparse.DenseArray)
	go calcPartitioning(SOxchan, pSchan)
	NH3chan := make(chan *sparse.DenseArray)
	pNHchan := make(chan *sparse.DenseArray)
	go calcPartitioning(NH3chan, pNHchan)

	// calculate NO/NO2 partitioning
	NOchan := make(chan *sparse.DenseArray)
	NO2chan := make(chan *sparse.DenseArray)
	go calcPartitioning(NOchan, NO2chan)

	// Get total PM2.5 averages for performance eval.
	totalpm25Chan := make(chan *sparse.DenseArray)
	go average(totalpm25Chan)

	qrainChan := make(chan *sparse.DenseArray)
	cloudFracChan := make(chan *sparse.DenseArray)
	altChanWetDep := make(chan *sparse.DenseArray)
	altChan := make(chan *sparse.DenseArray)
	go average(altChan)
	go calcWetDeposition(layerHeights, qrainChan, cloudFracChan, altChanWetDep)

	// Calculate stability for plume rise, vertical mixing,
	// and chemical reaction rates
	Tchan := make(chan *sparse.DenseArray)
	PBchan := make(chan *sparse.DenseArray)
	Pchan := make(chan *sparse.DenseArray)
	surfaceHeatFluxChan := make(chan *sparse.DenseArray)
	hoChan := make(chan *sparse.DenseArray)
	h2o2Chan := make(chan *sparse.DenseArray)
	luIndexChan := make(chan *sparse.DenseArray) // surface skin temp
	ustarChan := make(chan *sparse.DenseArray)
	altChanMixing := make(chan *sparse.DenseArray)
	qCloudChan := make(chan *sparse.DenseArray)
	swDownChan := make(chan *sparse.DenseArray)
	glwChan := make(chan *sparse.DenseArray)
	qrainChan2 := make(chan *sparse.DenseArray)
	pblhChan2 := make(chan *sparse.DenseArray)
	go StabilityMixingChemistry(layerHeights, pblhChan2,
		ustarChan, altChanMixing,
		Tchan, PBchan, Pchan, surfaceHeatFluxChan, hoChan, h2o2Chan,
		luIndexChan, qCloudChan, swDownChan, glwChan, qrainChan2)

	iterateTimeSteps("Reading data--pass 2: ",
		readSingleVar("U", deviationChanU),
		readSingleVar("V", deviationChanV),
		readGasGroup(aVOC, aVOCchan), readParticleGroup(aSOA, aSOAchan),
		readGasGroup(bVOC, bVOCchan), readParticleGroup(bSOA, bSOAchan),
		readGasGroup(NOx, NOxchan), readParticleGroup(pNO, pNOchan),
		readGasGroup(SOx, SOxchan), readParticleGroup(pS, pSchan),
		readGasGroup(NH3, NH3chan), readParticleGroup(pNH, pNHchan),
		readGasGroup(NO, NOchan), readGasGroup(NO2, NO2chan),
		readParticleGroup(totalPM25, totalpm25Chan),
		readSingleVar("HFX", surfaceHeatFluxChan),
		readSingleVar("UST", ustarChan),
		readSingleVar("PBLH", pblhChan2),
		readSingleVar("T", Tchan), readSingleVar("PB", PBchan),
		readSingleVar("P", Pchan), readSingleVar("ho", hoChan),
		readSingleVar("h2o2", h2o2Chan),
		readSingleVar("LU_INDEX", luIndexChan),
		readSingleVar("QRAIN", qrainChan, qrainChan2),
		readSingleVar("CLDFRA", cloudFracChan),
		readSingleVar("QCLOUD", qCloudChan),
		readSingleVar("ALT", altChanWetDep, altChanMixing, altChan),
		readSingleVar("SWDOWN", swDownChan),
		readSingleVar("GLW", glwChan))

	// speed deviation results
	deviationChanU <- nil
	deviationChanV <- nil
	uDeviation := <-deviationChanU
	vDeviation := <-deviationChanV

	// partitioning results
	aVOCchan <- nil
	aOrgPartitioning := <-aVOCchan
	aVOC := <-aVOCchan
	aSOA := <-aVOCchan
	bVOCchan <- nil
	bOrgPartitioning := <-bVOCchan
	bVOC := <-bVOCchan
	bSOA := <-bVOCchan
	NOxchan <- nil
	NOPartitioning := <-NOxchan
	gNO := <-NOxchan
	pNO := <-NOxchan
	SOxchan <- nil
	SPartitioning := <-SOxchan
	gS := <-SOxchan
	pS := <-SOxchan
	NH3chan <- nil
	NHPartitioning := <-NH3chan
	gNH := <-NH3chan
	pNH := <-NH3chan

	NOchan <- nil
	NONO2partitioning := <-NOchan
	<-NOchan
	<-NOchan

	// StabilityMixingChemistry results
	Tchan <- nil
	temperature := <-Tchan
	Sclass := <-Tchan
	S1 := <-Tchan
	Kzz := <-Tchan
	M2u := <-Tchan
	M2d := <-Tchan
	SO2oxidation := <-Tchan
	particleDryDep := <-Tchan
	SO2DryDep := <-Tchan
	NOxDryDep := <-Tchan
	NH3DryDep := <-Tchan
	VOCDryDep := <-Tchan
	Kxxyy := <-Tchan

	// average total pm2.5
	totalpm25Chan <- nil
	totalpm25 := <-totalpm25Chan

	// average inverse density
	altChan <- nil
	alt := <-altChan

	// wet deposition results
	qrainChan <- nil
	particleWetDep := <-qrainChan
	SO2WetDep := <-qrainChan
	otherGasWetDep := <-qrainChan

	// write out data to file
	outputFile := filepath.Join(config.OutputDir, config.OutputFilePrefix+".ncf")
	fmt.Printf("Writing out data to %v...\n", outputFile)

	data := new(inmap.CTMData)
	data.AddVariable("UAvg", []string{"z", "y", "xStagger"},
		"Annual average x velocity", "m/s", uAvg)
	data.AddVariable("VAvg", []string{"z", "yStagger", "x"},
		"Annual average y velocity", "m/s", vAvg)
	data.AddVariable("WAvg", []string{"zStagger", "y", "x"},
		"Annual average z velocity", "m/s", wAvg)
	data.AddVariable("UDeviation", []string{"z", "y", "xStagger"},
		"Average deviation from average x velocity", "m/s", uDeviation)
	data.AddVariable("VDeviation", []string{"z", "yStagger", "x"},
		"Average deviation from average y velocity", "m/s", vDeviation)
	data.AddVariable("aOrgPartitioning", []string{"z", "y", "x"},
		"Mass fraction of anthropogenic organic matter in particle {vs. gas} phase",
		"fraction", aOrgPartitioning)
	data.AddVariable("aVOC", []string{"z", "y", "x"},
		"Average anthropogenic VOC concentration", "ug m-3", aVOC)
	data.AddVariable("aSOA", []string{"z", "y", "x"},
		"Average anthropogenic secondary organic aerosol concentration", "ug m-3", aSOA)
	data.AddVariable("bOrgPartitioning", []string{"z", "y", "x"},
		"Mass fraction of biogenic organic matter in particle {vs. gas} phase",
		"fraction", bOrgPartitioning)
	data.AddVariable("bVOC", []string{"z", "y", "x"},
		"Average biogenic VOC concentration", "ug m-3", bVOC)
	data.AddVariable("bSOA", []string{"z", "y", "x"},
		"Average biogenic secondary organic aerosol concentration", "ug m-3", bSOA)
	data.AddVariable("NOPartitioning", []string{"z", "y", "x"},
		"Mass fraction of N from NOx in particle {vs. gas} phase", "fraction",
		NOPartitioning)
	data.AddVariable("gNO", []string{"z", "y", "x"},
		"Average concentration of nitrogen fraction of gaseous NOx", "ug m-3",
		gNO)
	data.AddVariable("pNO", []string{"z", "y", "x"},
		"Average concentration of nitrogen fraction of particulate NO3",
		"ug m-3", pNO)
	data.AddVariable("SPartitioning", []string{"z", "y", "x"},
		"Mass fraction of S from SOx in particle {vs. gas} phase", "fraction",
		SPartitioning)
	data.AddVariable("gS", []string{"z", "y", "x"},
		"Average concentration of sulfur fraction of gaseous SOx", "ug m-3",
		gS)
	data.AddVariable("pS", []string{"z", "y", "x"},
		"Average concentration of sulfur fraction of particulate sulfate",
		"ug m-3", pS)
	data.AddVariable("NHPartitioning", []string{"z", "y", "x"},
		"Mass fraction of N from NH3 in particle {vs. gas} phase", "fraction",
		NHPartitioning)
	data.AddVariable("gNH", []string{"z", "y", "x"},
		"Average concentration of nitrogen fraction of gaseous ammonia",
		"ug m-3", gNH)
	data.AddVariable("pNH", []string{"z", "y", "x"},
		"Average concentration of nitrogen fraction of particulate ammonium",
		"ug m-3", pNH)
	data.AddVariable("NO_NO2partitioning", []string{"z", "y", "x"},
		"Mass fraction of N in NOx that exists as NO.", "fraction",
		NONO2partitioning)
	data.AddVariable("SO2oxidation", []string{"z", "y", "x"},
		"Rate of SO2 oxidation to SO4 by hydroxyl radical and H2O2",
		"s-1", SO2oxidation)
	data.AddVariable("ParticleDryDep", []string{"z", "y", "x"},
		"Dry deposition velocity for particles", "m s-1", particleDryDep)
	data.AddVariable("SO2DryDep", []string{"z", "y", "x"},
		"Dry deposition velocity for SO2", "m s-1", SO2DryDep)
	data.AddVariable("NOxDryDep", []string{"z", "y", "x"},
		"Dry deposition velocity for NOx", "m s-1", NOxDryDep)
	data.AddVariable("NH3DryDep", []string{"z", "y", "x"},
		"Dry deposition velocity for NH3", "m s-1", NH3DryDep)
	data.AddVariable("VOCDryDep", []string{"z", "y", "x"},
		"Dry deposition velocity for VOCs", "m s-1", VOCDryDep)
	data.AddVariable("Kxxyy", []string{"z", "y", "x"},
		"Horizontal eddy diffusion coefficient", "m2 s-1", Kxxyy)
	data.AddVariable("LayerHeights", []string{"zStagger", "y", "x"},
		"Height at edge of layer", "m", layerHeights)
	data.AddVariable("Dz", []string{"z", "y", "x"},
		"Vertical grid size", "m", Dz)
	data.AddVariable("ParticleWetDep", []string{"z", "y", "x"},
		"Wet deposition rate constant for fine particles",
		"s-1", particleWetDep)
	data.AddVariable("SO2WetDep", []string{"z", "y", "x"},
		"Wet deposition rate constant for SO2 gas", "s-1", SO2WetDep)
	data.AddVariable("OtherGasWetDep", []string{"z", "y", "x"},
		"Wet deposition rate constant for other gases", "s-1", otherGasWetDep)
	data.AddVariable("Kzz", []string{"zStagger", "y", "x"},
		"Vertical turbulent diffusivity", "m2 s-1", Kzz)
	data.AddVariable("M2u", []string{"z", "y", "x"},
		"ACM2 nonlocal upward mixing {Pleim 2007}", "s-1", M2u)
	data.AddVariable("M2d", []string{"z", "y", "x"},
		"ACM2 nonlocal downward mixing {Pleim 2007}", "s-1", M2d)
	data.AddVariable("Pblh", []string{"y", "x"},
		"Planetary boundary layer height", "m", pblh)
	data.AddVariable("WindSpeed", []string{"z", "y", "x"},
		"RMS wind speed", "m s-1", windSpeed)
	data.AddVariable("WindSpeedInverse", []string{"z", "y", "x"},
		"RMS wind speed^(-1)", "(m s-1)^(-1)", windSpeedInverse)
	data.AddVariable("WindSpeedMinusThird", []string{"z", "y", "x"},
		"RMS wind speed^(-1/3)", "(m s-1)^(-1/3)", windSpeedMinusThird)
	data.AddVariable("WindSpeedMinusOnePointFour", []string{"z", "y", "x"},
		"RMS wind speed^(-1.4)", "(m s-1)^(-1.4)", windSpeedMinusOnePointFour)
	data.AddVariable("Temperature", []string{"z", "y", "x"},
		"Average Temperature", "K", temperature)
	data.AddVariable("S1", []string{"z", "y", "x"},
		"Stability parameter", "?", S1)
	data.AddVariable("Sclass", []string{"z", "y", "x"},
		"Stability parameter", "0=Unstable; 1=Stable", Sclass)
	data.AddVariable("alt", []string{"z", "y", "x"},
		"Inverse density", "m3 kg-1", alt)
	data.AddVariable("TotalPM25", []string{"z", "y", "x"},
		"Total PM2.5 concentration", "ug m-3", totalpm25)

	ff, err := os.Create(outputFile)
	if err != nil {
		panic(err)
	}
	data.Write(ff, config.CtmGridXo, config.CtmGridYo, config.CtmGridDx, config.CtmGridDy)
	ff.Close()
}

func iterateTimeSteps(msg string, funcs ...cdfReaderFunc) {
	filechan := make(chan string)
	finishchan := make(chan int)
	numTsteps = 0
	for i := 0; i < runtime.GOMAXPROCS(0); i++ {
		go processFile(filechan, finishchan, funcs...)
	}
	delta, _ := time.ParseDuration("24h")
	for now := start; now.Before(end); now = now.Add(delta) {
		d := now.Format(wrfFormat)
		log.Println(msg + d + "...")
		file := strings.Replace(config.Wrfout, "[DATE]", d, -1)
		filechan <- file
	}
	close(filechan)
	for i := 0; i < runtime.GOMAXPROCS(0); i++ {
		numTsteps += float64(<-finishchan)
	}
}

func processFile(filechan chan string, finishchan chan int,
	funcs ...cdfReaderFunc) {
	f := new(cdfFile)
	var err error
	tsteps := 0
	for filename := range filechan {
		f.ff, err = os.Open(filename)
		if err != nil {
			log.Printf("WARNING: couldn't read file %s", filename)
			continue
		}
		f.f, err = cdf.Open(f.ff)
		if err != nil {
			panic(err)
		}
		for hour := 0; hour < 24; hour++ {
			var wg sync.WaitGroup
			wg.Add(len(funcs))
			for _, fn := range funcs {
				go fn(f, hour, &wg)
			}
			wg.Wait()
		}
		tsteps += 24
		f.ff.Close()
	}
	finishchan <- tsteps
}

type cdfReaderFunc func(*cdfFile, int, *sync.WaitGroup)

type cdfFile struct {
	f       *cdf.File
	ff      *os.File
	ncfLock sync.Mutex
}

// read a variable out of a netcdf file.
func readNCF(pol string, f *cdfFile, hour int) (data *sparse.DenseArray) {
	dims := f.f.Header.Lengths(pol)
	if len(dims) == 0 {
		panic(fmt.Sprintf("Variable %v not in file.", pol))
	}
	dims = dims[1:]
	nread := 1
	for _, dim := range dims {
		nread *= dim
	}
	start, end := make([]int, len(dims)+1), make([]int, len(dims)+1)
	start[0], end[0] = hour, hour+1
	r := f.f.Reader(pol, start, end)
	buf := r.Zero(nread)
	f.ncfLock.Lock()
	_, err := r.Read(buf)
	f.ncfLock.Unlock()
	if err != nil {
		panic(err)
	}
	data = sparse.ZerosDense(dims...)
	for i, val := range buf.([]float32) {
		data.Elements[i] = float64(val)
	}
	return
}

// readSingleVar creates a function that reads a single variable
// out of a netcdf file.
func readSingleVar(Var string,
	datachans ...chan *sparse.DenseArray) cdfReaderFunc {
	return func(f *cdfFile, hour int, wg *sync.WaitGroup) {
		defer wg.Done()
		data := readNCF(Var, f, hour)
		for _, datachan := range datachans {
			datachan <- data
		}
	}
}

func readGasGroup(Vars map[string]float64, datachans ...chan *sparse.DenseArray) cdfReaderFunc {
	return func(f *cdfFile, hour int, wg *sync.WaitGroup) {
		defer wg.Done()
		alt := readNCF("ALT", f, hour) // inverse density (m3 kg-1)
		var out *sparse.DenseArray
		firstData := true
		for pol, factor := range Vars {
			data := readNCF(pol, f, hour)
			if firstData {
				out = sparse.ZerosDense(data.Shape...)
				firstData = false
			}
			for i, val := range data.Elements {
				// convert ppm to μg/m3
				out.Elements[i] += val * factor / MWa * 1000. / alt.Elements[i]
			}
		}
		for _, datachan := range datachans {
			datachan <- out
		}
	}
}

func readParticleGroup(Vars map[string]float64, datachans ...chan *sparse.DenseArray) cdfReaderFunc {
	return func(f *cdfFile, hour int, wg *sync.WaitGroup) {
		defer wg.Done()
		alt := readNCF("ALT", f, hour) // inverse density (m3 kg-1)
		var out *sparse.DenseArray
		firstData := true
		for pol, factor := range Vars {
			data := readNCF(pol, f, hour)
			if firstData {
				out = sparse.ZerosDense(data.Shape...)
				firstData = false
			}
			for i, val := range data.Elements {
				// convert μg/kg air to μg/m3 air
				out.Elements[i] += val * factor / alt.Elements[i]
			}
		}
		for _, datachan := range datachans {
			datachan <- out
		}
	}
}

// Calculate marginal partitioning
func calcPartitioning(gaschan, particlechan chan *sparse.DenseArray) {
	var gas, particle, oldgas, oldparticle, partitioning *sparse.DenseArray
	firstData := true
	for {
		gasdata := <-gaschan
		if gasdata == nil {
			// Divide the arrays by the total number of timesteps and return.
			gaschan <- arrayAverage(partitioning)
			gaschan <- arrayAverage(gas)
			gaschan <- arrayAverage(particle)
			return
		}
		particledata := <-particlechan
		if firstData {
			// In the first time step, just copy the arrays to the
			// old arrays; don't do any calculations.
			partitioning = sparse.ZerosDense(gasdata.Shape...)
			gas = sparse.ZerosDense(gasdata.Shape...)
			particle = sparse.ZerosDense(gasdata.Shape...)
			oldgas = gasdata.Copy()
			oldparticle = particledata.Copy()
			firstData = false
			continue
		}
		gas.AddDense(gasdata)
		particle.AddDense(particledata)
		for i, particleval := range particledata.Elements {
			particlechange := particleval - oldparticle.Elements[i]
			totalchange := particlechange + (gasdata.Elements[i] -
				oldgas.Elements[i])
			// Calculate the marginal partitioning coefficient, which is the
			// change in particle concentration divided by the change in overall
			// concentration. Force the coefficient to be between zero and
			// one.
			part := math.Min(math.Max(particlechange/totalchange, 0), 1)
			if !math.IsNaN(part) {
				partitioning.Elements[i] += part
			}
		}
		oldgas = gasdata.Copy()
		oldparticle = particledata.Copy()
	}
}

// Calculate fraction of the time that the atmosphere is ammonia-poor, where
// total ammonia [moles] < 2 * total sulfur VI [moles].
func ammoniaStatus(NH3chan, pNHchan, pSchan chan *sparse.DenseArray) {
	var fracAmmoniaPoor *sparse.DenseArray
	firstData := true
	for {
		nh3array := <-NH3chan // μg/m3 N
		pnharray := <-pNHchan // μg/m3 N
		psarray := <-pSchan   // μg/m3 S
		if nh3array == nil {
			NH3chan <- arrayAverage(fracAmmoniaPoor)
			return
		}
		if firstData {
			fracAmmoniaPoor = sparse.ZerosDense(nh3array.Shape...)
			firstData = false
		}
		for i, nh3 := range nh3array.Elements {
			pnh := pnharray.Elements[i]
			ps := psarray.Elements[i]
			var poor float64
			if (nh3+pnh)/mwN < 2.*ps/mwS {
				poor = 1.
			} else {
				poor = 0.
			}
			fracAmmoniaPoor.Elements[i] += poor
		}
	}
}

func average(datachan chan *sparse.DenseArray) {
	var avgdata *sparse.DenseArray
	firstData := true
	for {
		data := <-datachan
		if data == nil {
			for i, val := range avgdata.Elements {
				avgdata.Elements[i] = val / numTsteps
			}
			datachan <- avgdata
			return
		}
		if firstData {
			avgdata = sparse.ZerosDense(data.Shape...)
			firstData = false
		}
		avgdata.AddDense(data)
	}
}

// calcLayerHeights calculates the heights above the ground
// of the layers (in meters).
// For more information, refer to
// http://www.openwfm.org/wiki/How_to_interpret_WRF_variables
func calcLayerHeights(ph, phb *sparse.DenseArray) (
	layerHeights, Dz *sparse.DenseArray) {
	layerHeights = sparse.ZerosDense(ph.Shape...)
	Dz = sparse.ZerosDense(ph.Shape[0]-1, ph.Shape[1], ph.Shape[2])
	for k := 0; k < ph.Shape[0]; k++ {
		for j := 0; j < ph.Shape[1]; j++ {
			for i := 0; i < ph.Shape[2]; i++ {
				h := (ph.Get(k, j, i) + phb.Get(k, j, i) -
					ph.Get(0, j, i) - phb.Get(0, j, i)) / g // m
				layerHeights.Set(h, k, j, i)
				if k > 0 {
					Dz.Set(h-layerHeights.Get(k-1, j, i), k-1, j, i)
				}
			}
		}
	}
	return
}

// calculate wet deposition
func calcWetDeposition(layerHeights *sparse.DenseArray, qrainChan,
	cloudFracChan, altChan chan *sparse.DenseArray) {
	var wdParticle, wdSO2, wdOtherGas *sparse.DenseArray

	Δz := sparse.ZerosDense(layerHeights.Shape[0]-1, layerHeights.Shape[1],
		layerHeights.Shape[2]) // grid cell height, m
	for k := 0; k < layerHeights.Shape[0]-1; k++ {
		for j := 0; j < layerHeights.Shape[1]; j++ {
			for i := 0; i < layerHeights.Shape[2]; i++ {
				Δz.Set(layerHeights.Get(k+1, j, i)-layerHeights.Get(k, j, i),
					k, j, i)
			}
		}
	}

	firstData := true
	for {
		qrain := <-qrainChan // mass frac
		if qrain == nil {
			qrainChan <- arrayAverage(wdParticle)
			qrainChan <- arrayAverage(wdSO2)
			qrainChan <- arrayAverage(wdOtherGas)
			return
		}
		cloudFrac := <-cloudFracChan // frac
		alt := <-altChan             // m3/kg
		if firstData {
			wdParticle = sparse.ZerosDense(qrain.Shape...) // units = 1/s
			wdSO2 = sparse.ZerosDense(qrain.Shape...)      // units = 1/s
			wdOtherGas = sparse.ZerosDense(qrain.Shape...) // units = 1/s
			firstData = false
		}
		for i := 0; i < len(qrain.Elements); i++ {
			wdp, wds, wdo := emep.WetDeposition(cloudFrac.Elements[i],
				qrain.Elements[i], 1/alt.Elements[i], Δz.Elements[i])
			wdParticle.Elements[i] += wdp
			wdSO2.Elements[i] += wds
			wdOtherGas.Elements[i] += wdo
		}
	}
}

// windDeviation calculates the average absolute deviation of the wind velocity.
// Output is based on a staggered grid.
// index dictates which directional index (i, j, or k) that
// we are calculating deviations in.
func windDeviation(uAvg *sparse.DenseArray, uChan chan *sparse.DenseArray, index int) {
	var uDeviation *sparse.DenseArray
	firstData := true
	for {
		u := <-uChan
		if u == nil {
			uChan <- arrayAverage(uDeviation)
			return
		}
		if firstData {
			uDeviation = sparse.ZerosDense(u.Shape...)
			firstData = false
		}
		for i, uV := range u.Elements {
			avgV := uAvg.Elements[i]
			uDeviation.Elements[i] += math.Abs(uV - avgV)
		}
	}
}

func arrayAverage(s *sparse.DenseArray) *sparse.DenseArray {
	for i, val := range s.Elements {
		s.Elements[i] = val / numTsteps
	}
	return s
}

// windSpeed calculates RMS wind speed as well as average speeds in each
// direction.
func windSpeed(uChan, vChan, wChan chan *sparse.DenseArray) {
	var speed *sparse.DenseArray
	var speedInverse *sparse.DenseArray
	var speedMinusThird *sparse.DenseArray
	var speedMinusOnePointFour *sparse.DenseArray
	var uAvg, vAvg, wAvg *sparse.DenseArray
	firstData := true
	var dims []int
	for {
		u := <-uChan
		v := <-vChan
		w := <-wChan
		if u == nil {
			for i := range speed.Elements {
				speed.Elements[i] /= numTsteps
				speedInverse.Elements[i] /= numTsteps
				speedMinusThird.Elements[i] /= numTsteps
				speedMinusOnePointFour.Elements[i] /= numTsteps
			}
			uChan <- speed
			uChan <- speedInverse
			uChan <- speedMinusThird
			uChan <- speedMinusOnePointFour
			uChan <- arrayAverage(uAvg)
			vChan <- arrayAverage(vAvg)
			wChan <- arrayAverage(wAvg)
			return
		}
		if firstData {
			uAvg = sparse.ZerosDense(u.Shape...)
			vAvg = sparse.ZerosDense(v.Shape...)
			wAvg = sparse.ZerosDense(w.Shape...)
			// get unstaggered grid sizes
			dims = make([]int, len(u.Shape))
			for i, ulen := range u.Shape {
				vlen := v.Shape[i]
				wlen := w.Shape[i]
				dims[i] = minInt(ulen, vlen, wlen)
			}
			speed = sparse.ZerosDense(dims...)
			speedInverse = sparse.ZerosDense(dims...)
			speedMinusThird = sparse.ZerosDense(dims...)
			speedMinusOnePointFour = sparse.ZerosDense(dims...)
			firstData = false
		}
		uAvg.AddDense(u)
		vAvg.AddDense(v)
		wAvg.AddDense(w)
		for k := 0; k < dims[0]; k++ {
			for j := 0; j < dims[1]; j++ {
				for i := 0; i < dims[2]; i++ {
					ucenter := (math.Abs(u.Get(k, j, i)) +
						math.Abs(u.Get(k, j, i+1))) / 2.
					vcenter := (math.Abs(v.Get(k, j, i)) +
						math.Abs(v.Get(k, j+1, i))) / 2.
					wcenter := (math.Abs(w.Get(k, j, i)) +
						math.Abs(w.Get(k+1, j, i))) / 2.
					s := math.Pow(math.Pow(ucenter, 2.)+
						math.Pow(vcenter, 2.)+math.Pow(wcenter, 2.), 0.5)
					speed.AddVal(s, k, j, i)
					speedInverse.AddVal(1./s, k, j, i)
					speedMinusThird.AddVal(math.Pow(s, -1./3.), k, j, i)
					speedMinusOnePointFour.AddVal(math.Pow(s, -1.4), k, j, i)
				}
			}
		}
	}
}

// USGSz0 holds Roughness lengths for USGS land classes ([m]), from WRF file
// VEGPARM.TBL.
var USGSz0 = []float64{.50, .1, .06, .1, 0.095, .20, .11,
	.03, .035, .15, .50, .50, .50, .50, .35, 0.0001, .20, .40,
	.01, .10, .30, .15, .075, 0.001, .01, .15, .01}

// USGSseinfeld lookup table to go from USGS land classes to land classes for
// particle dry deposition.
var USGSseinfeld = []seinfeld.LandUseCategory{
	seinfeld.Desert,    //'Urban and Built-Up Land'
	seinfeld.Grass,     //'Dryland Cropland and Pasture'
	seinfeld.Grass,     //'Irrigated Cropland and Pasture'
	seinfeld.Grass,     //'Mixed Dryland/Irrigated Cropland and Pasture'
	seinfeld.Grass,     //'Cropland/Grassland Mosaic'
	seinfeld.Grass,     //'Cropland/Woodland Mosaic'
	seinfeld.Grass,     //'Grassland'
	seinfeld.Shrubs,    //'Shrubland'
	seinfeld.Shrubs,    //'Mixed Shrubland/Grassland'
	seinfeld.Grass,     //'Savanna'
	seinfeld.Deciduous, //'Deciduous Broadleaf Forest'
	seinfeld.Evergreen, //'Deciduous Needleleaf Forest'
	seinfeld.Deciduous, //'Evergreen Broadleaf Forest'
	seinfeld.Evergreen, //'Evergreen Needleleaf Forest'
	seinfeld.Deciduous, //'Mixed Forest'
	seinfeld.Desert,    //'Water Bodies'
	seinfeld.Grass,     //'Herbaceous Wetland'
	seinfeld.Deciduous, //'Wooded Wetland'
	seinfeld.Desert,    //'Barren or Sparsely Vegetated'
	seinfeld.Shrubs,    //'Herbaceous Tundra'
	seinfeld.Deciduous, //'Wooded Tundra'
	seinfeld.Shrubs,    //'Mixed Tundra'
	seinfeld.Desert,    //'Bare Ground Tundra'
	seinfeld.Desert,    //'Snow or Ice'
	seinfeld.Desert,    //'Playa'
	seinfeld.Desert,    //'Lava'
	seinfeld.Desert}    //'White Sand'

// USGSwesely lookup table to go from USGS land classes to land classes for
// gas dry deposition.
var USGSwesely = []wesely1989.LandUseCategory{
	wesely1989.Urban,        //'Urban and Built-Up Land'
	wesely1989.RangeAg,      //'Dryland Cropland and Pasture'
	wesely1989.RangeAg,      //'Irrigated Cropland and Pasture'
	wesely1989.RangeAg,      //'Mixed Dryland/Irrigated Cropland and Pasture'
	wesely1989.RangeAg,      //'Cropland/Grassland Mosaic'
	wesely1989.Agricultural, //'Cropland/Woodland Mosaic'
	wesely1989.Range,        //'Grassland'
	wesely1989.RockyShrubs,  //'Shrubland'
	wesely1989.RangeAg,      //'Mixed Shrubland/Grassland'
	wesely1989.Range,        //'Savanna'
	wesely1989.Deciduous,    //'Deciduous Broadleaf Forest'
	wesely1989.Coniferous,   //'Deciduous Needleleaf Forest'
	wesely1989.Deciduous,    //'Evergreen Broadleaf Forest'
	wesely1989.Coniferous,   //'Evergreen Needleleaf Forest'
	wesely1989.MixedForest,  //'Mixed Forest'
	wesely1989.Water,        //'Water Bodies'
	wesely1989.Wetland,      //'Herbaceous Wetland'
	wesely1989.Wetland,      //'Wooded Wetland'
	wesely1989.Barren,       //'Barren or Sparsely Vegetated'
	wesely1989.RockyShrubs,  //'Herbaceous Tundra'
	wesely1989.MixedForest,  //'Wooded Tundra'
	wesely1989.RockyShrubs,  //'Mixed Tundra'
	wesely1989.Barren,       //'Bare Ground Tundra'
	wesely1989.Barren,       //'Snow or Ice'
	wesely1989.Barren,       //'Playa'
	wesely1989.Barren,       //'Lava'
	wesely1989.Barren}       //'White Sand'

// StabilityMixingChemistry Calculates:
// 1) Stability parameters for use in plume rise calculation (ASME, 1973,
// as described in Seinfeld and Pandis, 2006).
// 2) Vertical turbulent diffusivity using a middling value (1 m2/s)
// from Wilson (2004) for grid cells above the planetary boundary layer
// and Pleim (2007) for grid cells within the planetary
// boundary layer.
// 3) SO2 oxidation to SO4 by HO (Stockwell 1997).
// 4) Dry deposition velocity (gocart and Seinfed and Pandis (2006)).
// 5) Horizontal eddy diffusion coefficient (Kyy, [m2/s]) assumed to be the
// same as vertical eddy diffusivity.
//
// Inputs are layer heights (m), friction velocity (ustar, m/s),
// planetary boundary layer height (pblh, m), inverse density (m3/kg),
// perturbation potential temperature (Temp,K), Pressure (Pb and P, Pa),
// surface heat flux (W/m2), HO mixing ratio (ppmv), and USGS land use index
// (luIndex).
func StabilityMixingChemistry(LayerHeights *sparse.DenseArray,
	pblhChan, ustarChan, altChan, Tchan, PBchan, Pchan, surfaceHeatFluxChan,
	hoChan, h2o2Chan, luIndexChan,
	qCloudChan, swDownChan, glwChan, qrainChan chan *sparse.DenseArray) {
	const (
		po    = 101300. // Pa, reference pressure
		kappa = 0.2854  // related to von karman's constant
		Cp    = 1006.   // m2/s2-K; specific heat of air
	)

	var Temp *sparse.DenseArray
	var S1 *sparse.DenseArray
	var Sclass *sparse.DenseArray
	var Kzz *sparse.DenseArray
	var M2d *sparse.DenseArray
	var M2u *sparse.DenseArray
	var SO2oxidation *sparse.DenseArray
	var particleDryDep *sparse.DenseArray
	var SO2DryDep *sparse.DenseArray
	var NOxDryDep *sparse.DenseArray
	var NH3DryDep *sparse.DenseArray
	var VOCDryDep *sparse.DenseArray
	var Kyy *sparse.DenseArray
	firstData := true
	for {
		T := <-Tchan  // K
		if T == nil { // done reading data: return results
			// Check for mass balance in convection coefficients
			for k := 0; k < M2u.Shape[0]-2; k++ {
				for j := 0; j < M2u.Shape[1]; j++ {
					for i := 0; i < M2u.Shape[2]; i++ {
						z := LayerHeights.Get(k, j, i)
						zabove := LayerHeights.Get(k+1, j, i)
						z2above := LayerHeights.Get(k+2, j, i)
						Δzratio := (z2above - zabove) / (zabove - z)
						m2u := M2u.Get(k, j, i)
						val := m2u - M2d.Get(k, j, i) +
							M2d.Get(k+1, j, i)*Δzratio
						if math.Abs(val/m2u) > 1.e-8 {
							panic(fmt.Errorf("M2u and M2d don't match: "+
								"(k,j,i)=(%v,%v,%v); val=%v; m2u=%v; "+
								"m2d=%v, m2dAbove=%v",
								k, j, i, val, m2u, M2d.Get(k, j, i),
								M2d.Get(k+1, j, i)))
						}
					}
				}
			}
			// convert Kzz to unstaggered grid
			KzzUnstaggered := sparse.ZerosDense(Temp.Shape...)
			for j := 0; j < KzzUnstaggered.Shape[1]; j++ {
				for i := 0; i < KzzUnstaggered.Shape[2]; i++ {
					for k := 0; k < KzzUnstaggered.Shape[0]; k++ {
						KzzUnstaggered.Set(
							(Kzz.Get(k, j, i)+Kzz.Get(k+1, j, i))/2.,
							k, j, i)
					}
				}
			}
			for _, arr := range []*sparse.DenseArray{Temp,
				Sclass, S1, KzzUnstaggered, M2u, M2d, SO2oxidation,
				particleDryDep, SO2DryDep, NOxDryDep, NH3DryDep, VOCDryDep, Kyy} {
				Tchan <- arrayAverage(arr)
			}
			return
		}
		PB := <-PBchan               // Pa
		P := <-Pchan                 // Pa
		hfx := <-surfaceHeatFluxChan // W/m2
		ho := <-hoChan               // ppmv
		h2o2 := <-h2o2Chan           // ppmv
		luIndex := <-luIndexChan     // land use index
		ustar := <-ustarChan         // friction velocity (m/s)
		pblh := <-pblhChan           // current boundary layer height (m)
		alt := <-altChan             // inverse density (m3/kg)
		qCloud := <-qCloudChan       // cloud water mixing ratio (kg/kg)
		swDown := <-swDownChan       // Downwelling short wave at ground level (W/m2)
		glw := <-glwChan             // Downwelling long wave at ground level (W/m2)
		qrain := <-qrainChan         // mass fraction rain
		if firstData {
			Temp = sparse.ZerosDense(T.Shape...) // units = K
			S1 = sparse.ZerosDense(T.Shape...)
			Sclass = sparse.ZerosDense(T.Shape...)
			Kzz = sparse.ZerosDense(LayerHeights.Shape...) // units = m2/s
			M2u = sparse.ZerosDense(T.Shape...)            // units = 1/s
			M2d = sparse.ZerosDense(T.Shape...)            // units = 1/s
			SO2oxidation = sparse.ZerosDense(T.Shape...)   // units = 1/s
			particleDryDep = sparse.ZerosDense(T.Shape...) // units = m/s
			SO2DryDep = sparse.ZerosDense(T.Shape...)      // units = m/s
			NOxDryDep = sparse.ZerosDense(T.Shape...)      // units = m/s
			NH3DryDep = sparse.ZerosDense(T.Shape...)      // units = m/s
			VOCDryDep = sparse.ZerosDense(T.Shape...)      // units = m/s
			Kyy = sparse.ZerosDense(T.Shape...)            // units = m2/s
			firstData = false
		}
		type empty struct{}
		sem := make(chan empty, T.Shape[1]) // semaphore pattern
		for j := 0; j < T.Shape[1]; j++ {
			go func(j int) { // concurrent processing
				for i := 0; i < T.Shape[2]; i++ {
					// Get Layer index of PBL top (staggered)
					var pblTop int
					for k := 0; k < LayerHeights.Shape[0]; k++ {
						if LayerHeights.Get(k, j, i) >= pblh.Get(j, i) {
							pblTop = k
							break
						}
					}
					// Calculate boundary layer average temperature (K)
					To := 0.
					for k := 0; k < LayerHeights.Shape[0]; k++ {
						if k == pblTop {
							To /= float64(k)
							break
						}
						To += T.Get(k, j, i) + 300.
					}
					// Calculate convective mixing rate
					u := ustar.Get(j, i) // friction velocity
					h := LayerHeights.Get(pblTop, j, i)
					hflux := hfx.Get(j, i)                // heat flux [W m-2]
					ρ := 1 / alt.Get(0, j, i)             // density [kg/m3]
					L := acm2.ObukhovLen(hflux, ρ, To, u) // Monin-Obukhov length [m]
					fconv := acm2.ConvectiveFraction(L, h)
					m2u := acm2.M2u(LayerHeights.Get(1, j, i),
						LayerHeights.Get(2, j, i), h, L, u, fconv)

					// Calculate dry deposition
					p := (P.Get(0, j, i) + PB.Get(0, j, i)) // Pressure [Pa]
					//z: [m] surface layer; assumed to be 10% of boundary layer.
					z := h / 10.
					// z: [m] surface layer; assumed to be top of first model layer.
					//z := LayerHeights.Get(1, j, i)
					lu := f2i(luIndex.Get(j, i))
					//gocartObk := gocart.ObhukovLen(hflux, ρ, To, u)
					zo := USGSz0[lu]         // roughness length [m]
					const dParticle = 0.3e-6 // [m], Seinfeld & Pandis fig 8.11
					const ρparticle = 1830.  // [kg/m3] Jacobson (2005) Ex. 13.5
					const Θsurface = 0.      // surface slope [rad]; Assume surface is flat.

					// This is not the best way to tell what season it is.
					var iSeasonP seinfeld.SeasonalCategory // for particles
					var iSeasonG wesely1989.SeasonCategory // for gases
					switch {
					case To > 273.+20.:
						iSeasonP = seinfeld.Midsummer
						iSeasonG = wesely1989.Midsummer
					case To <= 273.+20 && To > 273.+10.:
						iSeasonP = seinfeld.Autumn
						iSeasonG = wesely1989.Autumn
					case To <= 273.+10 && To > 273.+0.:
						iSeasonP = seinfeld.LateAutumn
						iSeasonG = wesely1989.LateAutumn
					default:
						iSeasonP = seinfeld.Winter
						iSeasonG = wesely1989.Winter
					}
					const dew = false // don't know if there's dew.
					rain := qrain.Get(0, j, i) > 1.e-6

					G := swDown.Get(j, i) + glw.Get(j, i) // irradiation [W/m2]
					particleDryDep.AddVal(
						//gocart.ParticleDryDep(gocartObk, u, To, h,
						//	zo, dParticle/2., ρparticle, p), 0, j, i)
						seinfeld.DryDepParticle(z, zo, u, L, dParticle,
							To, p, ρparticle,
							ρ, iSeasonP, USGSseinfeld[lu]), 0, j, i)
					SO2DryDep.AddVal(
						seinfeld.DryDepGas(z, zo, u, L, To, ρ,
							G, Θsurface,
							wesely1989.So2Data, iSeasonG,
							USGSwesely[lu], rain, dew, true, false), 0, j, i)
					NOxDryDep.AddVal(
						seinfeld.DryDepGas(z, zo, u, L, To, ρ,
							G, Θsurface,
							wesely1989.No2Data, iSeasonG,
							USGSwesely[lu], rain, dew, false, false), 0, j, i)
					NH3DryDep.AddVal(
						seinfeld.DryDepGas(z, zo, u, L, To, ρ,
							G, Θsurface,
							wesely1989.Nh3Data, iSeasonG,
							USGSwesely[lu], rain, dew, false, false), 0, j, i)
					VOCDryDep.AddVal(
						seinfeld.DryDepGas(z, zo, u, L, To, ρ,
							G, Θsurface,
							wesely1989.OraData, iSeasonG,
							USGSwesely[lu], rain, dew, false, false), 0, j, i)

					for k := 0; k < T.Shape[0]; k++ {
						Tval := T.Get(k, j, i)
						var dthetaDz = 0. // potential temperature gradient
						if k < T.Shape[0]-1 {
							dthetaDz = (T.Get(k+1, j, i) - Tval) /
								(LayerHeights.Get(k+1, j, i) -
									LayerHeights.Get(k, j, i)) // K/m
						}

						p := P.Get(k, j, i) + PB.Get(k, j, i) // Pa
						pressureCorrection := math.Pow(p/po, kappa)

						// potential temperature, K
						θ := Tval + 300.
						// Ambient temperature, K
						t := θ * pressureCorrection
						Temp.AddVal(t, k, j, i)

						// Stability parameter
						s1 := dthetaDz / t * pressureCorrection
						S1.AddVal(s1, k, j, i)

						// Stability class
						if dthetaDz < 0.005 {
							Sclass.AddVal(0., k, j, i)
						} else {
							Sclass.AddVal(1., k, j, i)
						}

						// Mixing
						z := LayerHeights.Get(k, j, i)
						zabove := LayerHeights.Get(k+1, j, i)
						zcenter := (LayerHeights.Get(k, j, i) +
							LayerHeights.Get(k+1, j, i)) / 2
						Δz := zabove - z

						const freeAtmKzz = 3. // [m2 s-1]
						if k >= pblTop {      // free atmosphere (unstaggered grid)
							Kzz.AddVal(freeAtmKzz, k, j, i)
							Kyy.AddVal(freeAtmKzz, k, j, i)
							if k == T.Shape[0]-1 { // Top Layer
								Kzz.AddVal(freeAtmKzz, k+1, j, i)
							}
						} else { // Boundary layer (unstaggered grid)
							Kzz.AddVal(acm2.Kzz(z, h, L, u, fconv), k, j, i)
							M2d.AddVal(acm2.M2d(m2u, z, Δz, h), k, j, i)
							M2u.AddVal(m2u, k, j, i)
							kmyy := acm2.CalculateKm(zcenter, h, L, u)
							Kyy.AddVal(kmyy, k, j, i)
						}

						// Gas phase sulfur chemistry
						const Na = 6.02214129e23 // molec./mol (Avogadro's constant)
						const cm3perm3 = 100. * 100. * 100.
						const molarMassAir = 28.97 / 1000.             // kg/mol
						const airFactor = molarMassAir / Na * cm3perm3 // kg/molec.* cm3/m3
						M := 1. / (alt.Get(k, j, i) * airFactor)       // molec. air / cm3
						hoConc := ho.Get(k, j, i) * 1.e-6 * M          // molec. HO / cm3
						// SO2 oxidation rate (Stockwell 1997, Table 2d)
						const kinf = 1.5e-12
						ko := 3.e-31 * math.Pow(t/300., -3.3)
						SO2rate := (ko * M / (1 + ko*M/kinf)) * math.Pow(0.6,
							1./(1+math.Pow(math.Log10(ko*M/kinf), 2.))) // cm3/molec/s
						kso2 := SO2rate * hoConc

						// Aqueous phase sulfur chemistry
						qCloudVal := qCloud.Get(k, j, i)
						if qCloudVal > 0. {
							const pH = 3.5 // doesn't really matter for SO2
							qCloudVal /=
								alt.Get(k, j, i) * 1000. // convert to volume frac.
							kso2 += seinfeld.SulfurH2O2aqueousOxidationRate(
								h2o2.Get(k, j, i)*1000., pH, t, p*atmPerPa,
								qCloudVal)
						}
						SO2oxidation.AddVal(kso2, k, j, i) // 1/s
					}

					// Check for mass balance in convection coefficients
					for k := 0; k < M2u.Shape[0]-2; k++ {
						z := LayerHeights.Get(k, j, i)
						zabove := LayerHeights.Get(k+1, j, i)
						z2above := LayerHeights.Get(k+2, j, i)
						Δzratio := (z2above - zabove) / (zabove - z)
						m2u := M2u.Get(k, j, i)
						val := m2u - M2d.Get(k, j, i) +
							M2d.Get(k+1, j, i)*Δzratio
						if math.Abs(val/m2u) > 1.e-8 {
							panic(fmt.Errorf("M2u and M2d don't match: "+
								"(k,j,i)=(%v,%v,%v); val=%v; m2u=%v; "+
								"m2d=%v, m2dAbove=%v; kpbl=%v",
								k, j, i, val, m2u, M2d.Get(k, j, i),
								M2d.Get(k+1, j, i), pblTop))
						}
					}

				}
				sem <- empty{}
			}(j)
		}
		for j := 0; j < T.Shape[1]; j++ { // wait for routines to finish
			<-sem
		}
	}
}

func minInt(vals ...int) int {
	minval := vals[0]
	for _, val := range vals {
		if val < minval {
			minval = val
		}
	}
	return minval
}

// convert float to int (rounding)
func f2i(f float64) int {
	return int(f + 0.5)
}

// ReadConfigFile Reads and parses a json configuration file.
func ReadConfigFile(filename string) {
	// Open the configuration file
	var (
		file  *os.File
		bytes []byte
		err   error
	)
	file, err = os.Open(filename)
	if err != nil {
		fmt.Printf("The configuration file you have specified, %v, does not "+
			"appear to exist. Please check the file name and location and "+
			"try again.\n", filename)
		os.Exit(1)
	}
	reader := bufio.NewReader(file)
	bytes, err = ioutil.ReadAll(reader)
	if err != nil {
		panic(err)
	}

	err = json.Unmarshal(bytes, &config)
	if err != nil {
		fmt.Printf(
			"There has been an error parsing the configuration file.\n"+
				"Please ensure that the file is in valid JSON format\n"+
				"(you can check for errors at http://jsonlint.com/)\n"+
				"and try again!\n\n%v\n\n", err.Error())
		os.Exit(1)
	}

	err = os.MkdirAll(config.OutputDir, os.ModePerm)
	if err != nil {
		log.Println(err.Error())
		os.Exit(1)
	}
	return
}
