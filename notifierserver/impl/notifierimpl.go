package impl

import (
	"context"
	"fmt"
	util "github.com/aldelo/common"
	pb "github.com/aldelo/connector/notifierserver/proto"
	"google.golang.org/grpc/grpclog"
	"log"
	"sync"
)

type StreamConnection struct {
	Id string
	Topic string
	Stream pb.Notifier_SubscribeServer
	Active bool
	Err chan error
}

type NotifierImpl struct {
	pb.UnimplementedNotifierServer

	Subscribers map[string][]*StreamConnection
}

func (n *NotifierImpl) Subscribe(s *pb.NotificationSubscriber, serverStream pb.Notifier_SubscribeServer) error {
	if n.Subscribers == nil {
		n.Subscribers = make(map[string][]*StreamConnection)
	}

	sc := &StreamConnection{
		Id: s.Id,
		Topic: s.Topic,
		Stream: serverStream,
		Active: true,
		Err: make(chan error),
	}

	key := s.Topic

	if util.LenTrim(key) == 0 {
		key = "no-topic-defined"
	}

	scList := n.Subscribers[key]
	scList = append(scList, sc)
	n.Subscribers[key] = scList

	return <-sc.Err
}

func (n *NotifierImpl) Broadcast(c context.Context, d *pb.NotificationData) (r *pb.NotificationDone, err error) {
	if n.Subscribers == nil {
		return &pb.NotificationDone{}, fmt.Errorf("Broadcast Cancelled, No Subscribers Found On This Server")
	}

	topic := d.Topic

	if util.LenTrim(topic) == 0 {
		return &pb.NotificationDone{}, fmt.Errorf("Broadcast Cancelled, Input Notification Data Missing Topic")
	}

	scList := n.Subscribers[topic]

	if len(scList) == 0 {
		return &pb.NotificationDone{}, fmt.Errorf("Broadcast Cancelled, No Subscribers Found For Topic '%s'", topic)
	}

	wg := sync.WaitGroup{}
	done := make(chan int)

	log.Println("Begin Broadcasting to " + util.Itoa(len(scList)) + " Subscribers On Topic '" + topic + "'...")

	for _, sc := range scList {
		wg.Add(1)

		go func(data *pb.NotificationData, streamConn *StreamConnection) {
			defer wg.Done()

			if streamConn.Active {
				if e := streamConn.Stream.Send(data); e != nil {
					grpclog.Errorf("Broadcasting Data with ID '%s' to Subscriber '%s' For Topic '%s' Failed: %s", data.Id, streamConn.Id, streamConn.Topic, e.Error())

					streamConn.Active = false
					streamConn.Err <- e
				} else {
					grpclog.Infof("Broadcasting Data with ID '%s' to Subscriber '%s' For Topic '%s' Completed", data.Id, streamConn.Id, streamConn.Topic)
				}
			} else {
				grpclog.Infof("Broadcasting Data with ID '%s' to Subscriber '%s' For Topic '%s' Skipped (StreamConnection Non-Active)", data.Id, streamConn.Id, streamConn.Topic)
			}
		}(d, sc)
	}

	go func() {
		wg.Wait()
		close(done)
	}()

	<-done

	log.Println("... End Broadcasting to " + util.Itoa(len(scList)) + " Subscribers On Topic '" + topic + "'")

	return &pb.NotificationDone{}, nil
}
