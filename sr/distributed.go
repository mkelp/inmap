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

package sr

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"net/rpc"
	"os"

	"github.com/ctessum/geom"
	"github.com/spatialmodel/inmap"
)

// Empty is used for passing content-less messages.
type Empty struct{}

// RPCPort specifies the port for RPC communications. The default is
// 6060.
var RPCPort = "6060"

// Worker is a worker for performing InMAP simulations. It should not be interacted
// with directly, but it is exported to meet RPC requirements.
type Worker struct {
	Config     *inmap.VarGridConfig
	CTMData    *inmap.CTMData
	Pop        *inmap.Population
	PopIndices inmap.PopIndices
	MR         *inmap.MortalityRates
	GridGeom   []geom.Polygonal // Geometry of the output grid.
}

// IOData holds the input to and output from a simulation request.
type IOData struct {
	Emis       *inmap.Emissions
	Output     map[string][]float64
	Row, Layer int
}

// Result allows a local worker to look like a distributed request
func (io *IOData) Result() (interface{}, error) {
	return io, nil
}

// Calculate performs an InMAP simulation. It meets the requirements for
// use with rpc.Call.
func (s *Worker) Calculate(input *IOData, output *IOData) error {
	log.Printf("Slave calculating row=%v, layer=%v\n", input.Row, input.Layer)

	scienceFuncs := inmap.Calculations(
		inmap.UpwindAdvection(),
		inmap.Mixing(),
		inmap.MeanderMixing(),
		inmap.DryDeposition(),
		inmap.WetDeposition(),
		inmap.Chemistry(),
	)

	initFuncs := []inmap.DomainManipulator{
		s.Config.RegularGrid(s.CTMData, s.Pop, s.PopIndices, s.MR, input.Emis),
		inmap.SetTimestepCFL(),
	}
	const gridMutateInterval = 3600. // seconds
	runFuncs := []inmap.DomainManipulator{
		inmap.Calculations(inmap.AddEmissionsFlux()),
		scienceFuncs,
		inmap.RunPeriodically(gridMutateInterval,
			s.Config.MutateGrid(inmap.PopConcMutator(
				s.Config.PopConcThreshold, s.Config, s.PopIndices),
				s.CTMData, s.Pop, s.MR, input.Emis)),
		inmap.RunPeriodically(gridMutateInterval, inmap.SetTimestepCFL()),
		inmap.SteadyStateConvergenceCheck(-1, nil),
	}

	d := &inmap.InMAP{
		InitFuncs: initFuncs,
		RunFuncs:  runFuncs,
	}

	if err := d.Init(); err != nil {
		return fmt.Errorf("InMAP: problem initializing model: %v\n", err)
	}

	if err := d.Run(); err != nil {
		return fmt.Errorf("InMAP: problem running simulation: %v\n", err)
	}

	output.Output = make(map[string][]float64)
	output.Row = input.Row
	output.Layer = input.Layer
	o, err := d.Results(false, outputVars...)
	if err != nil {
		return err
	}
	g := d.GetGeometry(0, false)
	for pol, data := range o {
		d, err := inmap.Regrid(g, s.GridGeom, data)
		if err != nil {
			return err
		}
		output.Output[pol] = d
	}
	return nil
}

// Exit shuts down the worker. It meets the requirements for
// use with rpc.Call.
func (s *Worker) Exit(in, out interface{}) error {
	os.Exit(0)
	return nil
}

// NewWorker sets up an RPC listener for performing simulations.
// InMAPDataFile specifies
// the location of the inmap regular-gridded data, and GridGeom specifies the
// output grid geometry.
func NewWorker(config *inmap.VarGridConfig, InMAPDataFile string, GridGeom []geom.Polygonal) (*Worker, error) {
	s := new(Worker)
	s.Config = config
	s.GridGeom = GridGeom
	f, err := os.Open(InMAPDataFile)
	if err != nil {
		return nil, fmt.Errorf("problem loading input data: %v\n", err)
	}
	s.CTMData, err = s.Config.LoadCTMData(f)
	if err != nil {
		return nil, fmt.Errorf("problem loading input data: %v\n", err)
	}

	log.Println("Loading population and mortality rate data")
	s.Pop, s.PopIndices, s.MR, err = s.Config.LoadPopMort()
	if err != nil {
		return nil, fmt.Errorf("problem loading population or mortality data: %v", err)
	}
	return s, nil
}

// Listen directs s to start listening for requests over RPCPort
func (s *Worker) Listen(RPCPort string) error {
	rpc.Register(s)
	rpc.HandleHTTP()
	l, err := net.Listen("tcp", ":"+RPCPort)
	if err != nil {
		return err
	}
	log.Println("Started slave")
	return http.Serve(l, nil)
}
