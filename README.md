# (In)tervention (M)odel for (A)ir (P)ollution

[![Build Status](https://travis-ci.org/spatialmodel/inmap.svg?branch=master)](https://travis-ci.org/spatialmodel/inmap) [![Coverage Status](https://coveralls.io/repos/github/spatialmodel/inmap/badge.svg?branch=master)](https://coveralls.io/github/spatialmodel/inmap?branch=master) [![GoDoc](http://godoc.org/github.com/spatialmodel/inmap?status.svg)](http://godoc.org/github.com/spatialmodel/inmap) [![Go Report Card](https://goreportcard.com/badge/github.com/spatialmodel/inmap)](https://goreportcard.com/report/github.com/spatialmodel/inmap)

## About InMAP

InMAP is a multi-scale emissions-to-health impact model for fine particulate matter (PM<sub>2.5</sub>) that mechanistically evaluates air quality and health benefits of perturbations to baseline emissions. A main simplification of InMAP compared to a comprehensive chemical transport model is that it does so on an annual-average basis rather than the highly time-resolved performance of a full CTM. The model incorporates annual-average parameters (e.g. transport, deposition, and reaction rates) from the WRF/Chem chemical transport model. Grid-cell size varies as shown in Figure 1, ranging from smaller grid cells in urban areas to larger grid cells in rural areas. This variable resolution grid is used to simulate population exposures to PM<sub>2.5</sub> with high spatial resolution while minimizing computational expense.

![alt tag](fig1.png?raw=true)
Figure 1: InMAP spatial discretization of the model domain into variable resolution grid cells. Left panel: full domain; right panel: a small section of the domain centered on the city of Los Angeles.


## Getting InMAP

Go to [releases](https://github.com/spatialmodel/inmap/releases) to download the most recent release for your type of computer. For Mac systems, download the file with "darwin" in the name. You will need both the executable program and the input data ("inmapData_xxx.zip"). All of the versions of the program are labeled "amd64" to denote that they are for 64-bit processors (i.e., all relatively recent notebook and desktop computers). It doesn't matter whether your computer processor is made by AMD or another brand, it should work either way.

### Compiling from source

You can also compile InMAP from its source code. It should work on most types of computers. Refer [here](http://golang.org/doc/install#requirements) for a list of theoretically supported systems. Instructions follow:

1. Install the [Go compiler](http://golang.org/doc/install). Make sure you install the correct version (64 bit) for your system. Also make sure to set the [`$GOPATH`](http://golang.org/doc/code.html#GOPATH) environment variable to a *different directory* than where the Go compiler is installed. It may be useful to go through one of the tutorials to make sure the compiler is correctly installed.

2. Make sure your `$PATH` environment variable includes the directories `$GOROOT/bin` and `$GOPATH/bin`. On Linux or Macintosh systems, this can be done using the command `export PATH=$PATH:$GOROOT/bin:$GOPATH/bin`. On Windows systems, you can follow [these](http://www.computerhope.com/issues/ch000549.htm) directions.

3. Install the [git](http://git-scm.com/) and [mercurial](http://mercurial.selenic.com/) version control programs, if they are not already installed. If you are using a shared system or cluster, you may just need to load them with the commands `module load git` and `module load hg`.

4. Download and install the main program:

		go get github.com/spatialmodel/inmap/inmap
	The Go language has an automatic system for finding and installing library dependencies; you may want to refer [here](http://golang.org/doc/code.html) to understand how it works.

5. Optional: run the tests:

		cd $GOPATH/src/github.com/spatialmodel/inmap
		go test ./...

## Running InMAP

1. Make sure that you have downloaded the InMAP input data files: `InMAPData_vX.X.X.zip` from the [InMAP release page](https://github.com/spatialmodel/inmap/releases), where X.X.X corresponds to a version number.

3. Create an emissions scenario or use one of the evaluation emissions datasets available in the `InMAPEvalData_vX.X.X.zip` files on the [InMAP release page](https://github.com/spatialmodel/inmap/releases). Emissions files should be in [shapefile](http://en.wikipedia.org/wiki/Shapefile) format where the attribute columns correspond to the names of emitted pollutants. Refer [here](http://godoc.org/github.com/spatialmodel/inmap#pkg-variables) (the `EmisNames` variable) for acceptable pollutant names. Emissions units can be specified in the configuration file (discussed below) and can be either  short tons or kilograms per year. The model can handle multiple input emissions files, and emissions can be either elevated or ground level. Files with elevated emissions need to have attribute columns labeled "height", "diam", "temp", and "velocity" containing stack information in units of m, m, K, and m/s, respectively. Emissions will be allocated from the geometries in the shape file to the InMAP computational grid.

1. Make a copy of the [configuration file template](inmap/configExample.json) and edit it so that the `VariableGridData`  variable points to the location where you downloaded the general corresponding input data file to, `EmissionsShapefiles` points to the location(s) of the emissions files, and the `OutputFile` variable points to the desired location for the output file. Refer to the source code documentation ([here](https://godoc.org/github.com/spatialmodel/inmap/inmap/cmd#ConfigData)) for information about other configuration options. The configuration file is a text file in [TOML](https://github.com/toml-lang/toml) format, and any changes made to the file will need to conform to that format or the model will not run correctly and will produce an error.

2. Run the program:

		inmapXXX run steady --config=/path/to/configfile.toml
	where `inmapXXX` is replaced with the executable file that you [downloaded](https://github.com/spatialmodel/inmap/releases). For some systems you may need to type `./inmapXXX` instead. If you compiled the program from source, the command will just be `inmap` for Linux or Mac systems and `inmap.exe` for Windows systems.

	The above command runs the model in the most typical mode. For alternative run modes and other command options refer [here](inmap/cmd/doc/inmap.md).


3. View the program output. The output files are in [shapefile](http://en.wikipedia.org/wiki/Shapefile) format which can be viewed in most GIS programs. One free GIS program is [QGIS](http://www.qgis.org/). By default, the InMAP only outputs results from layer zero, but this can be changed using the configuration file.
  Output variables are specified as `OutputVariables` in the configuration file. There is a complete list of options [here](OutputOptions.md). Some examples include:
	* Pollutant concentrations in units of μg m<sup>-3</sup>:
		* VOC (`VOC`)
		* NO<sub>x</sub> (`NOx`)
		* NH<sub>3</sub> (`NH3`)
		* SO<sub>x</sub> (`SOx`)
		* Total PM<sub>2.5</sub> (`Total PM2.5`; The sum of all PM<sub>2.5</sub> components)
		* Primary PM<sub>2.5</sub> (`Primary PM2.5`)
		* Particulate sulfate (`pSO4`)
		* Particulate nitrate (`pNO3`)
		* Particulate ammonium (`pNH4`)
		* Secondary organic aerosol (`SOA`)
	* Populations of different demographic subgroups in units of people per square meter. The included populations may vary but in the default dataset as of this writing the groups included are:
      * total population (`TotalPop`)
      * people identifying as black (`Black`), asian  (`Asian`), latino (`Latino`), native american or american indian (`Native`), non-latino white (`WhiteNoLat`) and everyone else (`Other`).
      * People living below the poverty time (`Poverty`) and people living at more than twice the poverty line (`TwoXPov`).
    * Numbers of deaths attributable to PM<sub>2.5</sub> in each of the populations in units of deaths/year, which are specified as `POPULATION deaths` in the configuratio file, where `POPULATION` is one of the populations specified above. Attribute names in shapfiles are limited to 11 characters, so, for example, deaths in the `TotalPop` population would be labeled `TotalPop de`, deaths in the `Black` population would be labeled `Black death`, and—interestingly—deaths in the `WhiteNoLat` population would be labeled `WhiteNoLat_1`.
    * Baseline mortality rate in units of deaths per year per 100,000 people (`AllCause`), which can be used for performing alternative health impact calculations.


## API

The InMAP package is split into an executable program and an application programming interface (API). The documentation [here](http://godoc.org/github.com/spatialmodel/inmap) shows the functions available in the API and how they work.
