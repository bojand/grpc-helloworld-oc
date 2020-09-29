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
	"strings"
	"syscall"
	"time"

	// "github.com/bojand/grpc-helloworld-oc/exporter"
	"contrib.go.opencensus.io/exporter/prometheus"
	pb "github.com/bojand/grpc-helloworld-oc/helloworld"
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

var sc = make(chan callSample)

// sayHello implements helloworld.GreeterServer.SayHello
func sayHello(ctx context.Context, in *pb.HelloRequest) (*pb.HelloReply, error) {
	name := in.GetName()
	log.Printf("Received: %v", name)

	parts := strings.Split(name, ":")
	wid := parts[len(parts)-1]

	// mu.Lock()
	// samples = append(samples, callSample{instant: time.Now(), workerID: wid})
	// mu.Unlock()

	sc <- callSample{instant: time.Now(), workerID: wid}

	return &pb.HelloReply{Message: "Hello " + in.GetName()}, nil
}

func main() {
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

	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigs
		done <- true
	}()

	go func() {
		mux := http.NewServeMux()
		mux.Handle("/metrics", pe)
		if err := http.ListenAndServe(":8888", mux); err != nil {
			log.Fatalf("Failed to run Prometheus /metrics endpoint: %v", err)
		}
	}()

	var samples = make([]callSample, 0, 100000000)

	go func() {
		for s := range sc {
			samples = append(samples, s)
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

	close(sc)

	fmt.Println("done!")

	sort.Slice(samples, func(i, j int) bool {
		// return samples[i].instant.Before(samples[j].instant)
		return samples[i].instant.UnixNano() < samples[j].instant.UnixNano()
	})

	printData(samples, name)

	aggrRPS(samples, name)
}

func aggrRPS(s []callSample, name string) {

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

	for _, s := range s {
		s := s

		// if s.instant.Before(end) {
		if s.instant.UnixNano() >= start.UnixNano() && s.instant.UnixNano() < end.UnixNano() {
			crps++
		} else {
			rpsSamples = append(rpsSamples, valSample{instant: end, value: crps})

			start = end
			end = start.Add(time.Second)
			crps = 0
		}
	}

	rpsSamples = append(rpsSamples,
		valSample{instant: end.Add(1 * time.Second), value: 0},
		valSample{instant: end.Add(2 * time.Second), value: 0},
		valSample{instant: end.Add(3 * time.Second), value: 0},
	)

	csv(rpsSamples, name)
	plot(rpsSamples, name, name)
}

func printData(data []callSample, name string) {
	file, err := os.Create(fmt.Sprintf("%s_%d_data.csv", name, time.Now().Unix()))
	if err != nil {
		panic(err)
	}

	defer file.Close()

	fmt.Fprintf(file, "%s,%s\n", "time", "workerID")
	for _, s := range data {
		s := s
		fmt.Fprintf(file, "%d,%v\n", s.instant.UnixNano(), s.workerID)
	}
	fmt.Fprintln(file)
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
					StrokeColor: chart.ColorAlternateBlue,
					FillColor:   chart.ColorAlternateBlue.WithAlpha(24),
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
