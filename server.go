package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	pb "github.com/bojand/grpc-helloworld-oc/helloworld"
	colorful "github.com/lucasb-eyer/go-colorful"
	"google.golang.org/grpc"

	chart "github.com/wcharczuk/go-chart"
	"github.com/wcharczuk/go-chart/drawing"
	"go.opencensus.io/plugin/ocgrpc"
)

const (
	port = ":50051"
)

type callSample struct {
	instant  time.Time
	workerID string
}

type valSample struct {
	instant time.Time
	value   uint64
}

// var sc = make(chan callSample)

var rps uint64

var wm sync.Mutex
var wrps map[string]uint64

var workerIDS []string
var workerIDSMap map[string]struct{}

var callSamples []*callSample

// sayHello implements helloworld.GreeterServer.SayHello
func sayHello(ctx context.Context, in *pb.HelloRequest) (*pb.HelloReply, error) {
	name := in.GetName()
	log.Printf("Received: %v", name)

	defer func() {
		atomic.AddUint64(&rps, 1)

		wm.Lock()
		defer wm.Unlock()

		if _, ok := workerIDSMap[name]; !ok {
			workerIDS = append(workerIDS, name)
			workerIDSMap[name] = struct{}{}
		}

		wrps[name] = wrps[name] + 1

		callSamples = append(callSamples, &callSample{instant: time.Now(), workerID: name})
	}()

	time.Sleep(50 * time.Millisecond)

	return &pb.HelloReply{Message: "Hello " + in.GetName()}, nil
}

func main() {
	wrps = make(map[string]uint64, 100)
	workerIDSMap = make(map[string]struct{}, 100)

	name := os.Args[1]

	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)
	stop := make(chan bool, 1)

	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigs
		stop <- true
	}()

	rpsSamples := make([]valSample, 0, 10000)

	wrpsSamples := make(map[time.Time]map[string]valSample, 10)

	ticker := time.NewTicker(1 * time.Second)

	go func() {
		for {
			select {
			case <-stop:
				ticker.Stop()
				done <- true
				return
			case <-ticker.C:
				instant := time.Now()
				ov := atomic.SwapUint64(&rps, 0)

				rpsSamples = append(rpsSamples, valSample{instant: instant, value: ov})

				wm.Lock()
				for k, v := range wrps {
					_, ok := wrpsSamples[instant]
					if !ok {
						wrpsSamples[instant] = make(map[string]valSample, 10)
					}

					wrpsSamples[instant][k] = valSample{instant: instant, value: v}

					delete(wrps, k)
				}
				wm.Unlock()
			}
		}
	}()

	go func() {
		lis, err := net.Listen("tcp", port)
		if err != nil {
			log.Fatalf("failed to listen: %v", err)
		}

		s := grpc.NewServer(grpc.StatsHandler(&ocgrpc.ServerHandler{}))
		pb.RegisterGreeterService(s, &pb.GreeterService{SayHello: sayHello})
		if err := s.Serve(lis); err != nil {
			log.Fatalf("failed to serve: %v", err)
		}
	}()

	<-done

	fmt.Println("done! workers:", len(workerIDS))

	hasHits := false

	sort.Slice(rpsSamples, func(i, j int) bool {
		if !hasHits && rpsSamples[i].value > 0 {
			hasHits = true
		}

		return rpsSamples[i].instant.UnixNano() < rpsSamples[j].instant.UnixNano()
	})

	if len(rpsSamples) > 0 && hasHits {
		// csv(rpsSamples, name)
		// plot(rpsSamples, name, name)
		plotW(wrpsSamples, name, name)
		plotWC(wrpsSamples, name, name)
		aggrRPS(callSamples, name)
	}
}

func aggrRPS(s []*callSample, name string) {

	cc := len(s)

	if cc == 0 {
		return
	}

	sort.Slice(s, func(i, j int) bool {
		return s[i].instant.UnixNano() < s[j].instant.UnixNano()
	})

	start := s[0].instant
	end := start.Add(time.Second)

	rpsSamples := make([]valSample, 0, 10000)

	rpsSamples = append(rpsSamples,
		valSample{instant: start.Add(-3 * time.Second), value: 0},
		valSample{instant: start.Add(-2 * time.Second), value: 0},
		valSample{instant: start.Add(-1 * time.Second), value: 0},
		valSample{instant: start, value: 0},
	)

	var crps uint64 = 0

	for i, s := range s {
		s := s
		nv := s.instant.UnixNano()

		// sv := start.UnixNano()
		// ev := end.UnixNano()
		// fmt.Println("start: ", sv, " end:", ev, " nv:", nv, " nv >= sv:", nv >= sv, " nv < ev:", nv < sv, " crps:", crps)

		// if s.instant.Before(end) {
		if nv >= start.UnixNano() && nv < end.UnixNano() {
			crps++

			if i == cc-1 {
				// end add manually
				// fmt.Println("add end", end.UnixNano(), crps)
				rpsSamples = append(rpsSamples, valSample{instant: end, value: crps})
			}
		} else if i == cc-1 {
			crps++

			// end add manually
			// fmt.Println("add end", end.UnixNano(), crps)
			rpsSamples = append(rpsSamples, valSample{instant: end, value: crps})
		} else {
			// fmt.Println("add ", end.UnixNano(), crps)
			rpsSamples = append(rpsSamples, valSample{instant: end, value: crps})

			start = end
			end = start.Add(time.Second)

			crps = 1 // we have a sample falling into the next one
		}
	}

	end = s[cc-1].instant

	rpsSamples = append(rpsSamples,
		valSample{instant: end.Add(1 * time.Second), value: 0},
		valSample{instant: end.Add(2 * time.Second), value: 0},
		valSample{instant: end.Add(3 * time.Second), value: 0},
	)

	total := uint64(0)
	for _, c := range rpsSamples {
		total = total + c.value
	}

	fmt.Println("aggr total:", total, " call count:", cc)

	name = name + "_aggr"
	csv(rpsSamples, name)
	plot(rpsSamples, name, name)
}

func csv(data []valSample, colY string) {
	csvFile, err := os.Create(fmt.Sprintf("%s_%d.csv", colY, time.Now().Unix()))
	if err != nil {
		panic(err)
	}

	defer csvFile.Close()

	fmt.Fprintf(csvFile, "%s,%s\n", "time", colY)
	for _, s := range data {
		s := s
		fmt.Fprintf(csvFile, "%s,%v\n", s.instant.Format(time.RFC3339), s.value)
	}
	fmt.Fprintln(csvFile)
}

func plot(data []valSample, name, yLabel string) {
	xValues := make([]time.Time, len(data))
	yValues := make([]float64, len(data))

	for i, v := range data {
		xValues[i] = v.instant
		yValues[i] = float64(v.value)
	}

	graph := chart.Chart{
		Width:  1200,
		Height: 480,
		XAxis: chart.XAxis{
			Name:           "Time",
			NameStyle:      chart.StyleShow(),
			ValueFormatter: chart.TimeValueFormatterWithFormat("01-02 3:04:05PM"),
			Style: chart.Style{
				Show:        true,
				StrokeWidth: 1,
				StrokeColor: drawing.Color{
					R: 85,
					G: 85,
					B: 85,
					A: 180,
				},
			},
		},
		YAxis: chart.YAxis{
			Name:      yLabel,
			NameStyle: chart.StyleShow(),
			Style: chart.Style{
				Show:        true,
				StrokeWidth: 1,
				StrokeColor: drawing.Color{
					R: 85,
					G: 85,
					B: 85,
					A: 180,
				},
			},
		},
		Series: []chart.Series{
			chart.TimeSeries{
				Name: name,
				Style: chart.Style{
					Show:        true,
					StrokeColor: chart.ColorBlue,
					FillColor:   chart.ColorBlue.WithAlpha(8),
				},
				XValues: xValues,
				YValues: yValues,
			},
		},
	}

	//note we have to do this as a separate step because we need a reference to graph
	graph.Elements = []chart.Renderable{
		chart.Legend(&graph),
	}

	pngFile, err := os.Create(fmt.Sprintf("%s_%d.png", name, time.Now().Unix()))
	if err != nil {
		panic(err)
	}

	if err := graph.Render(chart.PNG, pngFile); err != nil {
		panic(err)
	}

	if err := pngFile.Close(); err != nil {
		panic(err)
	}
}

func plotW(data map[time.Time]map[string]valSample, name, yLabel string) {

	xValues := make([]time.Time, 0, len(data))

	for t := range data {
		t := t
		xValues = append(xValues, t)
	}

	sort.Slice(xValues, func(i, j int) bool {
		return xValues[i].Before(xValues[j])
	})

	graph := chart.Chart{
		Width:  1200,
		Height: 480,
		XAxis: chart.XAxis{
			Name:           "Time",
			NameStyle:      chart.StyleShow(),
			ValueFormatter: chart.TimeValueFormatterWithFormat("01-02 3:04:05PM"),
			Style: chart.Style{
				Show:        true,
				StrokeWidth: 1,
				StrokeColor: drawing.Color{
					R: 85,
					G: 85,
					B: 85,
					A: 180,
				},
			},
		},
		YAxis: chart.YAxis{
			Name:      yLabel,
			NameStyle: chart.StyleShow(),
			Style: chart.Style{
				Show:        true,
				StrokeWidth: 1,
				StrokeColor: drawing.Color{
					R: 85,
					G: 85,
					B: 85,
					A: 180,
				},
			},
		},
	}

	pal, err := colorful.WarmPalette(len(workerIDS))
	if err != nil {
		panic(err)
	}

	ci := 0
	for _, wid := range workerIDS {
		wid := wid

		r, b, g, a := pal[ci].RGBA()
		col := drawing.ColorFromAlphaMixedRGBA(r, g, b, a)
		ci++

		yValues := make([]float64, 0, len(xValues))

		for _, tsv := range xValues {
			tsv := tsv
			tsdata, ok := data[tsv]
			if !ok {
				yValues = append(yValues, 0.0)
			} else {
				if wdata, ok := tsdata[wid]; ok {
					yValues = append(yValues, float64(wdata.value))
				} else {
					yValues = append(yValues, 0.0)
				}
			}
		}

		fmt.Println(wid, "rps: ", yValues)

		cs := chart.TimeSeries{
			Name: wid,
			Style: chart.Style{
				Show:        true,
				StrokeColor: col,
			},
			XValues: xValues,
			YValues: yValues,
		}
		graph.Series = append(graph.Series, cs)
	}

	pngFile, err := os.Create(fmt.Sprintf("%s_%d_workers_rps.png", name, time.Now().Unix()))
	if err != nil {
		panic(err)
	}

	if err := graph.Render(chart.PNG, pngFile); err != nil {
		panic(err)
	}

	if err := pngFile.Close(); err != nil {
		panic(err)
	}
}

func plotWC(data map[time.Time]map[string]valSample, name, yLabel string) {

	converted := make([]time.Time, 0, len(data))
	for t := range data {
		t := t
		converted = append(converted, t)
	}

	sort.Slice(converted, func(i, j int) bool {
		return converted[i].Before(converted[j])
	})

	xValues := make([]time.Time, len(converted)+2)
	xValues[0] = converted[0].Add(-1 * time.Second)
	for i, cv := range converted {
		cv := cv
		xValues[i+1] = cv
	}

	xValues[len(xValues)-1] = converted[len(converted)-1].Add(1 * time.Second)

	graph := chart.Chart{
		Width:  1200,
		Height: 480,
		XAxis: chart.XAxis{
			Name:           "Time",
			NameStyle:      chart.StyleShow(),
			ValueFormatter: chart.TimeValueFormatterWithFormat("01-02 3:04:05PM"),
			Style: chart.Style{
				Show:        true,
				StrokeWidth: 1,
				StrokeColor: drawing.Color{
					R: 85,
					G: 85,
					B: 85,
					A: 180,
				},
			},
		},
		YAxis: chart.YAxis{
			Name:      yLabel,
			NameStyle: chart.StyleShow(),
			Style: chart.Style{
				Show:        true,
				StrokeWidth: 1,
				StrokeColor: drawing.Color{
					R: 85,
					G: 85,
					B: 85,
					A: 180,
				},
			},
		},
	}

	yValues := make([]float64, 0, len(xValues))
	for _, tsv := range xValues {
		tsv := tsv
		tsdata, ok := data[tsv]
		if !ok {
			yValues = append(yValues, 0.0)
		} else {
			yValues = append(yValues, float64(len(tsdata)))
		}
	}

	fmt.Println("wc:", yValues)

	cs := chart.TimeSeries{
		Name: "worker_count",
		Style: chart.Style{
			Show: true,
		},
		XValues: xValues,
		YValues: yValues,
	}
	graph.Series = append(graph.Series, cs)

	pngFile, err := os.Create(fmt.Sprintf("%s_%d_wc.png", name, time.Now().Unix()))
	if err != nil {
		panic(err)
	}

	if err := graph.Render(chart.PNG, pngFile); err != nil {
		panic(err)
	}

	if err := pngFile.Close(); err != nil {
		panic(err)
	}
}
