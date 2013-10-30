package aim

import (
	"bitbucket.org/ctessum/sparse"
	"code.google.com/p/lvd.go/cdf"
	"fmt"
	"math"
	"math/rand"
	"os"
	"runtime"
	"sync"
	"time"
)

type AIMdata struct {
	Data              []*AIMcell   // One data holder for each grid cell
	nbins, Nx, Ny, Nz int          // number of meteorology bins
	arrayLock         sync.RWMutex // Avoid concentration arrays being written by one subroutine and read by another at the same time.
	Dt                float64      // seconds
	vs                float64      // Settling velocity, m/s
	VOCoxidationRate  float64      // VOC oxidation rate constant
	westBoundary      []*AIMcell   // boundary cells
	eastBoundary      []*AIMcell   // boundary cells
	northBoundary     []*AIMcell   // boundary cells
	southBoundary     []*AIMcell   // boundary cells
	topBoundary       []*AIMcell   // boundary cells; assume bottom boundary is the same as lowest layer
}

// Data for a single grid cell
type AIMcell struct {
	UbinsWest, UfreqWest           []float32 // m/s
	VbinsSouth, VfreqSouth         []float32 // m/s
	WbinsBelow, WfreqBelow         []float32 // m/s
	Uwest, Vsouth, Wbelow          float64
	orgPartitioning, SPartitioning float64   // gaseous fraction
	NOPartitioning, NHPartitioning float64   // gaseous fraction
	wdParticle, wdSO2, wdOtherGas  float64   // wet deposition rate, 1/s
	verticalDiffusivity            float64   // vertical diffusivity, m2/s
	Dx, Dy, Dz                     float64   // meters
	Volume                         float64   // cubic meters
	k, j, i                        int       // cell indicies
	ii                             int       // master index
	initialConc                    []float64 // concentrations at beginning of time step
	finalConc                      []float64 // concentrations at end of time step
	emisFlux                       []float64 //  emissions (μg/m3/s)
	WestNeighbor                   *AIMcell
	EastNeighbor                   *AIMcell
	SouthNeighbor                  *AIMcell
	NorthNeighbor                  *AIMcell
	BelowNeighbor                  *AIMcell
	AboveNeighbor                  *AIMcell
	GroundLevelNeighbor            *AIMcell
}

func newAIMcell(nbins int, dx, dy, dz float64) *AIMcell {
	c := new(AIMcell)
	c.Dx, c.Dy, c.Dz = dx, dy, dz
	c.Volume = dx * dy * dz
	c.UbinsWest = make([]float32, nbins)
	c.VbinsSouth = make([]float32, nbins)
	c.WbinsBelow = make([]float32, nbins)
	c.UfreqWest = make([]float32, nbins)
	c.VfreqSouth = make([]float32, nbins)
	c.WfreqBelow = make([]float32, nbins)
	c.initialConc = make([]float64, len(polNames))
	c.finalConc = make([]float64, len(polNames))
	c.emisFlux = make([]float64, len(polNames))
	return c
}

func InitAIMdata(filename string) *AIMdata {
	d := new(AIMdata)
	go d.WebServer()
	ff, err := os.Open(filename)
	if err != nil {
		panic(err)
	}
	defer ff.Close()
	f, err := cdf.Open(ff)
	if err != nil {
		panic(err)
	}
	d.nbins = f.Header.Lengths("Ubins")[0]
	dims := f.Header.Lengths("orgPartitioning")
	d.Nz = dims[0]
	d.Ny = dims[1]
	d.Nx = dims[2]
	dx, dy := 12000., 12000. // need to make these adjustable
	d.VOCoxidationRate = f.Header.GetAttribute("", "VOCoxidationRate").([]float64)[0]
	var wg sync.WaitGroup
	wg.Add(14)
	layerHeights := sparse.ZerosDense(d.Nz+1, d.Ny, d.Nx)
	readNCF(filename, &wg, "layerHeights", layerHeights)
	// set up data holders
	d.Data = make([]*AIMcell, d.Nz*d.Ny*d.Nx)
	ii := 0
	for k := 0; k < d.Nz; k++ {
		for j := 0; j < d.Ny; j++ {
			for i := 0; i < d.Nx; i++ {
				// calculate Dz (varies by layer)
				dz := layerHeights.Get(k+1, j, i) - layerHeights.Get(k, j, i)
				d.Data[ii] = newAIMcell(d.nbins, dx, dy, dz)
				d.Data[ii].k = k
				d.Data[ii].j = j
				d.Data[ii].i = i
				d.Data[ii].ii = ii
				ii++
			}
		}
	}
	// set up boundary data holders
	d.westBoundary = make([]*AIMcell, d.Nz*d.Ny)
	ii = 0
	i := 0
	for k := 0; k < d.Nz; k++ {
		for j := 0; j < d.Ny; j++ {
			d.westBoundary[ii] = newAIMcell(0, dx, dy, 0.)
			d.westBoundary[ii].k = k
			d.westBoundary[ii].j = j
			d.westBoundary[ii].i = i
			d.westBoundary[ii].ii = ii
			ii++
		}
	}
	d.eastBoundary = make([]*AIMcell, d.Nz*d.Ny)
	ii = 0
	i = d.Nx
	for k := 0; k < d.Nz; k++ {
		for j := 0; j < d.Ny; j++ {
			d.eastBoundary[ii] = newAIMcell(d.nbins, dx, dy, 0.)
			d.eastBoundary[ii].k = k
			d.eastBoundary[ii].j = j
			d.eastBoundary[ii].i = i
			d.eastBoundary[ii].ii = ii
			ii++
		}
	}
	d.southBoundary = make([]*AIMcell, d.Nz*d.Nx)
	ii = 0
	j := 0
	for k := 0; k < d.Nz; k++ {
		for i := 0; i < d.Nx; i++ {
			d.southBoundary[ii] = newAIMcell(0, dx, dy, 0.) // Don't allocate any bins for cells that don't need to have wind speeds
			d.southBoundary[ii].k = k
			d.southBoundary[ii].j = j
			d.southBoundary[ii].i = i
			d.southBoundary[ii].ii = ii
			ii++
		}
	}
	d.northBoundary = make([]*AIMcell, d.Nz*d.Nx)
	ii = 0
	j = d.Ny
	for k := 0; k < d.Nz; k++ {
		for i := 0; i < d.Nx; i++ {
			d.northBoundary[ii] = newAIMcell(d.nbins, dx, dy, 0.)
			d.northBoundary[ii].k = k
			d.northBoundary[ii].j = j
			d.northBoundary[ii].i = i
			d.northBoundary[ii].ii = ii
			ii++
		}
	}
	d.topBoundary = make([]*AIMcell, d.Ny*d.Nx)
	ii = 0
	k := d.Nz
	for j := 0; j < d.Ny; j++ {
		for i := 0; i < d.Nx; i++ {
			d.topBoundary[ii] = newAIMcell(d.nbins, dx, dy, 0.)
			d.topBoundary[ii].k = k
			d.topBoundary[ii].j = j
			d.topBoundary[ii].i = i
			d.topBoundary[ii].ii = ii
			ii++
		}
	}

	d.arrayLock.Lock()
	go d.readNCFbins(filename, &wg, "Ubins")
	go d.readNCFbins(filename, &wg, "Vbins")
	go d.readNCFbins(filename, &wg, "Wbins")
	go d.readNCFbins(filename, &wg, "Ufreq")
	go d.readNCFbins(filename, &wg, "Vfreq")
	go d.readNCFbins(filename, &wg, "Wfreq")
	go d.readNCF(filename, &wg, "orgPartitioning")
	go d.readNCF(filename, &wg, "SPartitioning")
	go d.readNCF(filename, &wg, "NOPartitioning")
	go d.readNCF(filename, &wg, "NHPartitioning")
	go d.readNCF(filename, &wg, "wdParticle")
	go d.readNCF(filename, &wg, "wdSO2")
	go d.readNCF(filename, &wg, "wdOtherGas")
	wg.Wait()
	d.arrayLock.Unlock()

	// Set up links to neighbors
	ii = 0
	var jj int
	for k := 0; k < d.Nz; k++ {
		for j := 0; j < d.Ny; j++ {
			for i := 0; i < d.Nx; i++ {
				if i == 0 {
					d.Data[ii].WestNeighbor = d.westBoundary[k*d.Ny+j]
				} else {
					jj = d.getIndex(k, j, i-1)
					d.Data[jj].checkIndicies(k, j, i-1)
					d.Data[ii].WestNeighbor = d.Data[jj]
				}
				if i == d.Nx-1 {
					d.Data[ii].EastNeighbor = d.eastBoundary[k*d.Ny+j]
				} else {
					jj = d.getIndex(k, j, i+1)
					d.Data[jj].checkIndicies(k, j, i+1)
					d.Data[ii].EastNeighbor = d.Data[jj]
				}
				if j == 0 {
					d.Data[ii].SouthNeighbor = d.southBoundary[k*d.Nx+i]
				} else {
					jj = d.getIndex(k, j-1, i)
					d.Data[jj].checkIndicies(k, j-1, i)
					d.Data[ii].SouthNeighbor = d.Data[jj]
				}
				if j == d.Ny-1 {
					d.Data[ii].NorthNeighbor = d.northBoundary[k*d.Nx+i]
				} else {
					jj = d.getIndex(k, j+1, i)
					d.Data[jj].checkIndicies(k, j+1, i)
					d.Data[ii].NorthNeighbor = d.Data[jj]
				}
				if k == 0 {
					d.Data[ii].BelowNeighbor = d.Data[ii] // assume bottom boundary is the same as lowest layer.
				} else {
					jj = d.getIndex(k-1, j, i)
					d.Data[jj].checkIndicies(k-1, j, i)
					d.Data[ii].BelowNeighbor = d.Data[jj]
				}
				if k == d.Nz-1 {
					d.Data[ii].AboveNeighbor = d.topBoundary[j*d.Nx+i]
				} else {
					jj = d.getIndex(k+1, j, i)
					d.Data[jj].checkIndicies(k+1, j, i)
					d.Data[ii].AboveNeighbor = d.Data[jj]
				}
				jj = d.getIndex(0, j, i)
				d.Data[jj].checkIndicies(0, j, i)
				d.Data[ii].GroundLevelNeighbor = d.Data[jj]
				ii++
			}
		}
	}
	d.SettlingVelocity()
	return d
}

// convert 3d index to 1d index
func (d *AIMdata) getIndex(k, j, i int) int {
	return k*d.Ny*d.Nx + j*d.Nx + i
}
func (c *AIMcell) checkIndicies(k, j, i int) {
	if k != c.k || j != c.j || k != c.k {
		panic(fmt.Sprintf("Expected indicies (%v,%v,%v) do not match actual "+
			"indicies (%v,%v,%v). Master index=%v.\n", k, j, i, c.k, c.j, c.i, c.ii))
	}
}

func setVelocities(cells []*AIMcell, nprocs, procNum int, wg *sync.WaitGroup) {
	defer wg.Done()
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	var random float32
	var c *AIMcell
	for ii := procNum; ii < len(cells); ii += nprocs {
		c = cells[ii]
		// choose bins using a weighted random method
		random = r.Float32()
		for b, bin := range c.UbinsWest {
			if random <= c.UfreqWest[b] {
				c.Uwest = float64(bin)
				break
			}
		}
		random = r.Float32()
		for b, bin := range c.VbinsSouth {
			if random <= c.VfreqSouth[b] {
				c.Vsouth = float64(bin)
				break
			}
		}
		random = r.Float32()
		for b, bin := range c.WbinsBelow {
			if random <= c.WfreqBelow[b] {
				c.Wbelow = float64(bin)
				break
			}
		}
	}
}

// Add in emissions flux to each cell at every time step
func (d *AIMdata) addEmissionsFlux(nprocs, procNum int) {
	var c *AIMcell
	for ii := procNum; ii < len(d.Data); ii += nprocs {
		c = d.Data[ii]
		for i, _ := range c.initialConc {
			c.finalConc[i] += c.emisFlux[i] * d.Dt
			c.initialConc[i] = c.finalConc[i]
		}
	}
}

// Add in emissions, set wind velocities using the weighted
// random walk method, copy initial concentrations to final concentrations,
// and set time step (dt).
func (d *AIMdata) SetupTimeStep() {
	d.arrayLock.Lock()
	var wg sync.WaitGroup
	nprocs := runtime.GOMAXPROCS(0) // number of processors
	wg.Add(nprocs * 4)
	// Set cell velocities.
	for procNum := 0; procNum < nprocs; procNum++ {
		go setVelocities(d.Data, nprocs, procNum, &wg)
	}
	// Set east, north, and top boundary velocities.
	for procNum := 0; procNum < nprocs; procNum++ {
		go setVelocities(d.eastBoundary, nprocs, procNum, &wg)
	}
	for procNum := 0; procNum < nprocs; procNum++ {
		go setVelocities(d.northBoundary, nprocs, procNum, &wg)
	}
	for procNum := 0; procNum < nprocs; procNum++ {
		go setVelocities(d.topBoundary, nprocs, procNum, &wg)
	}
	wg.Wait()

	d.setTstep(nprocs)
	// Add in emissions after we know dt.
	for procNum := 0; procNum < nprocs; procNum++ {
		go d.addEmissionsFlux(nprocs, procNum)
	}
	d.arrayLock.Unlock()
}

//  Set the time step using the Courant–Friedrichs–Lewy (CFL) condition.
func (d *AIMdata) setTstep(nprocs int) {
	const Cmax = 1
	valChan := make(chan float64)
	calcCFL := func(procNum int) {
		// don't worry about the edges of the staggered grids.
		var uval, vval, wval, thisval, val float64
		var c *AIMcell
		for ii := procNum; ii < len(d.Data); ii += nprocs {
			c = d.Data[ii]
			uval = math.Abs(c.Uwest) / c.Dx
			vval = math.Abs(c.Vsouth) / c.Dy
			wval = math.Abs(c.Wbelow) / c.Dz
			thisval = max(uval, vval, wval)
			if thisval > val {
				val = thisval
			}
		}
		valChan <- val
	}
	for procNum := 0; procNum < nprocs; procNum++ {
		go calcCFL(procNum)
	}
	val := 0.
	for i := 0; i < nprocs; i++ { // get max value from each processor
		procval := <-valChan
		if procval > val {
			val = procval
		}
	}
	d.Dt = Cmax / math.Pow(3., 0.5) / val // seconds
}

// Read variable which includes random walk bins from NetCDF file.
func (d *AIMdata) readNCFbins(filename string, wg *sync.WaitGroup, Var string) {
	defer wg.Done()
	dat := getNCFbuffer(filename, Var)
	var bstride, kstride, jstride int
	switch Var {
	case "Ubins", "Ufreq":
		bstride = d.Nz * d.Ny * (d.Nx + 1)
		kstride = d.Ny * (d.Nx + 1)
		jstride = d.Nx + 1
	case "Vbins", "Vfreq":
		bstride = d.Nz * (d.Ny + 1) * d.Nx
		kstride = (d.Ny + 1) * d.Nx
		jstride = d.Nx
	case "Wbins", "Wfreq":
		bstride = (d.Nz + 1) * d.Ny * d.Nx
		kstride = d.Ny * d.Nx
		jstride = d.Nx
	default:
		panic("Unexpected error!")
	}
	ii := 0
	var index int
	for k := 0; k < d.Nz; k++ {
		for j := 0; j < d.Ny; j++ {
			for i := 0; i < d.Nx; i++ {
				for b := 0; b < d.nbins; b++ {
					index = b*bstride + k*kstride + j*jstride + i
					switch Var {
					case "Ubins":
						d.Data[ii].UbinsWest[b] = dat[index]
					case "Ufreq":
						d.Data[ii].UfreqWest[b] = dat[index]
					case "Vbins":
						d.Data[ii].VbinsSouth[b] = dat[index]
					case "Vfreq":
						d.Data[ii].VfreqSouth[b] = dat[index]
					case "Wbins":
						d.Data[ii].WbinsBelow[b] = dat[index]
					case "Wfreq":
						d.Data[ii].WfreqBelow[b] = dat[index]
					default:
						panic(fmt.Sprintf("Variable %v unknown.\n", Var))
					}
				}
				ii++
			}
		}
	}
	// Set North, East, and Top edge velocity bins for Arakawa C-grid.
	ii = 0
	switch Var {
	case "Ubins":
		i := d.Nx
		for k := 0; k < d.Nz; k++ {
			for j := 0; j < d.Ny; j++ {
				for b := 0; b < d.nbins; b++ {
					index = b*bstride + k*kstride + j*jstride + i
					d.eastBoundary[ii].UbinsWest[b] = dat[index]
				}
				ii++
			}
		}
	case "Ufreq":
		i := d.Nx
		for k := 0; k < d.Nz; k++ {
			for j := 0; j < d.Ny; j++ {
				for b := 0; b < d.nbins; b++ {
					index = b*bstride + k*kstride + j*jstride + i
					d.eastBoundary[ii].UfreqWest[b] = dat[index]
				}
				ii++
			}
		}
	case "Vbins":
		j := d.Ny
		for k := 0; k < d.Nz; k++ {
			for i := 0; i < d.Nx; i++ {
				for b := 0; b < d.nbins; b++ {
					index = b*bstride + k*kstride + j*jstride + i
					d.northBoundary[ii].VbinsSouth[b] = dat[index]
				}
				ii++
			}
		}
	case "Vfreq":
		j := d.Ny
		for k := 0; k < d.Nz; k++ {
			for i := 0; i < d.Nx; i++ {
				for b := 0; b < d.nbins; b++ {
					index = b*bstride + k*kstride + j*jstride + i
					d.northBoundary[ii].VfreqSouth[b] = dat[index]
				}
				ii++
			}
		}
	case "Wbins":
		k := d.Nz
		for j := 0; j < d.Ny; j++ {
			for i := 0; i < d.Nx; i++ {
				for b := 0; b < d.nbins; b++ {
					index = b*bstride + k*kstride + j*jstride + i
					d.topBoundary[ii].WbinsBelow[b] = dat[index]
				}
				ii++
			}
		}
	case "Wfreq":
		k := d.Nz
		for j := 0; j < d.Ny; j++ {
			for i := 0; i < d.Nx; i++ {
				for b := 0; b < d.nbins; b++ {
					index = b*bstride + k*kstride + j*jstride + i
					d.topBoundary[ii].WfreqBelow[b] = dat[index]
				}
				ii++
			}
		}
	default:
		panic(fmt.Sprintf("Variable %v unknown.\n", Var))
	}
}

// Read variable from NetCDF file.
func (d *AIMdata) readNCF(filename string, wg *sync.WaitGroup, Var string) {
	defer wg.Done()
	dat := getNCFbuffer(filename, Var)
	kstride := d.Ny * d.Nx
	jstride := d.Nx
	ii := 0
	for k := 0; k < d.Nz; k++ {
		for j := 0; j < d.Ny; j++ {
			for i := 0; i < d.Nx; i++ {
				index := k*kstride + j*jstride + i
				switch Var {
				case "orgPartitioning":
					d.Data[ii].orgPartitioning = float64(dat[index])
				case "SPartitioning":
					d.Data[ii].SPartitioning = float64(dat[index])
				case "NOPartitioning":
					d.Data[ii].NOPartitioning = float64(dat[index])
				case "NHPartitioning":
					d.Data[ii].NHPartitioning = float64(dat[index])
				case "wdParticle":
					d.Data[ii].wdParticle = float64(dat[index])
				case "wdSO2":
					d.Data[ii].wdSO2 = float64(dat[index])
				case "wdOtherGas":
					d.Data[ii].wdOtherGas = float64(dat[index])
				default:
					panic(fmt.Sprintf("Variable %v unknown.\n", Var))
				}
				ii++
			}
		}
	}
}

func getNCFbuffer(filename string, Var string) []float32 {
	ff, err := os.Open(filename)
	if err != nil {
		panic(err)
	}
	f, err := cdf.Open(ff)
	if err != nil {
		panic(err)
	}
	dims := f.Header.Lengths(Var)
	defer ff.Close()
	nread := 1
	for _, dim := range dims {
		nread *= dim
	}
	r := f.Reader(Var, nil, nil)
	buf := r.Zero(nread)
	_, err = r.Read(buf)
	if err != nil {
		panic(err)
	}
	return buf.([]float32)
}