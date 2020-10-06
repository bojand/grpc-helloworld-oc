package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	// "github.com/bojand/grpc-helloworld-oc/exporter"
	"contrib.go.opencensus.io/exporter/prometheus"
	pb "github.com/bojand/grpc-helloworld-oc/helloworld"
	colorful "github.com/lucasb-eyer/go-colorful"
	"google.golang.org/grpc"

	chart "github.com/wcharczuk/go-chart"
	"github.com/wcharczuk/go-chart/drawing"
	"go.opencensus.io/plugin/ocgrpc"
	"go.opencensus.io/stats/view"
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

// sayHello implements helloworld.GreeterServer.SayHello
func sayHello(ctx context.Context, in *pb.HelloRequest) (*pb.HelloReply, error) {
	name := in.GetName()
	log.Printf("Received: %v", name)

	defer func() {
		atomic.AddUint64(&rps, 1)

		wm.Lock()
		defer wm.Unlock()

		if _, ok := wrps[name]; !ok {
			workerIDS = append(workerIDS, name)
		}

		wrps[name] = wrps[name] + 1
	}()

	time.Sleep(50 * time.Millisecond)

	return &pb.HelloReply{Message: "Hello " + in.GetName()}, nil
}

func main() {
	wrps = make(map[string]uint64, 100)

	name := os.Args[1]
	// view.SetReportingPeriod(30 * time.Second)

	// Register stats and trace exporters to export
	// the collected data.
	// view.RegisterExporter(&exporter.PrintExporter{})

	pe, err := prometheus.NewExporter(prometheus.Options{
		Namespace: "demo",
	})
	if err != nil {
		log.Fatalf("Failed to create Prometheus exporter: %v", err)
	}

	view.RegisterExporter(pe)

	if err := view.Register(ocgrpc.DefaultServerViews...); err != nil {
		log.Fatalf("Failed to register ocgrpc server views: %v", err)
	}

	sigs := make(chan os.Signal, 1)
	done := make(chan bool, 1)
	stop := make(chan bool, 1)

	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigs
		stop <- true
	}()

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", pe)
		if err := http.ListenAndServe(":8888", mux); err != nil {
			log.Fatalf("Failed to run Prometheus /metrics endpoint: %v", err)
		}
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

				rpsSamples = append(rpsSamples, valSample{instant: instant, value: atomic.LoadUint64(&rps)})
				atomic.StoreUint64(&rps, 0)

				wm.Lock()
				for k, v := range wrps {
					_, ok := wrpsSamples[instant]
					if !ok {
						wrpsSamples[instant] = make(map[string]valSample, 10)
					}

					wrpsSamples[instant][k] = valSample{instant: instant, value: v}

					wrps[k] = 0
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

	// close(sc)

	fmt.Println("done! workers:", len(workerIDS))

	hasHits := false

	sort.Slice(rpsSamples, func(i, j int) bool {
		if !hasHits && rpsSamples[i].value > 0 {
			hasHits = true
		}

		return rpsSamples[i].instant.UnixNano() < rpsSamples[j].instant.UnixNano()
	})

	if len(rpsSamples) > 0 && hasHits {
		csv(rpsSamples, name)
		plot(rpsSamples, name, name)
		plotW(wrpsSamples, name, name)
	}
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

	pal, err := colorful.WarmPalette(len(data))
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

		fmt.Println(wid, ": ", yValues)

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

	pngFile, err := os.Create(fmt.Sprintf("%s_%d_workers.png", name, time.Now().Unix()))
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
