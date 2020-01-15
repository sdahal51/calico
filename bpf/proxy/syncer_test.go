// Copyright (c) 2020 Tigera, Inc. All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package proxy_test

import (
	"net"
	"sync"

	"github.com/projectcalico/felix/bpf/nat"

	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/pkg/errors"
	"github.com/sirupsen/logrus"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	k8sp "k8s.io/kubernetes/pkg/proxy"

	"github.com/projectcalico/felix/bpf"
	proxy "github.com/projectcalico/felix/bpf/proxy"
	"github.com/projectcalico/felix/bpf/routes"
	"github.com/projectcalico/felix/ip"
)

func init() {
	logrus.SetOutput(GinkgoWriter)
	logrus.SetLevel(logrus.DebugLevel)
}

var _ = Describe("BPF Syncer", func() {
	svcs := newMockNATMap()
	eps := newMockNATBackendMap()

	nodeIPs := []net.IP{net.IPv4(192, 168, 0, 1), net.IPv4(10, 123, 0, 1)}
	rt := proxy.NewRTCache()

	s, _ := proxy.NewSyncer(nodeIPs, svcs, eps, rt)

	svcKey := k8sp.ServicePortName{
		NamespacedName: types.NamespacedName{
			Namespace: "default",
			Name:      "test-service",
		},
	}

	state := proxy.DPSyncerState{
		SvcMap: k8sp.ServiceMap{
			svcKey: &k8sp.BaseServiceInfo{
				ClusterIP: net.IPv4(10, 0, 0, 1),
				Port:      1234,
				Protocol:  v1.ProtocolTCP,
			},
		},
		EpsMap: k8sp.EndpointsMap{
			svcKey: []k8sp.Endpoint{&k8sp.BaseEndpointInfo{Endpoint: "10.1.0.1:5555"}},
		},
	}

	makestep := func(step func()) func() {
		return func() {
			defer func() {
				log("svcs = %+v\n", svcs)
				log("eps = %+v\n", eps)
			}()

			step()
		}
	}

	It("should make the right test transitions", func() {
		By("inserting a service with endpoint", makestep(func() {
			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			Expect(svcs.m).To(HaveLen(1))
			val, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 1), 1234, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val.Count()).To(Equal(uint32(1)))

			Expect(eps.m).To(HaveLen(1))
			bval, ok := eps.m[nat.NewNATBackendKey(val.ID(), 0)]
			Expect(ok).To(BeTrue())
			Expect(bval).To(Equal(nat.NewNATBackendValue(net.IPv4(10, 1, 0, 1), 5555)))
		}))

		svcKey2 := k8sp.ServicePortName{
			NamespacedName: types.NamespacedName{
				Namespace: "default",
				Name:      "second-service",
			},
		}

		By("inserting another service with multiple endpoints", makestep(func() {
			state.SvcMap[svcKey2] = &k8sp.BaseServiceInfo{
				ClusterIP: net.IPv4(10, 0, 0, 2),
				Port:      2222,
				Protocol:  v1.ProtocolTCP,
			}
			state.EpsMap[svcKey2] = []k8sp.Endpoint{
				&k8sp.BaseEndpointInfo{Endpoint: "10.2.0.1:1111"},
				&k8sp.BaseEndpointInfo{Endpoint: "10.2.0.1:2222"},
			}

			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			Expect(svcs.m).To(HaveLen(2))
			val, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 1), 1234, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val.Count()).To(Equal(uint32(1)))
			val, ok = svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 2), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val.Count()).To(Equal(uint32(2)))

			Expect(eps.m).To(HaveLen(3))
			Expect(eps.m).To(HaveKey(nat.NewNATBackendKey(val.ID(), 0)))
			Expect(eps.m).To(HaveKey(nat.NewNATBackendKey(val.ID(), 1)))
			Expect(eps.m).To(ContainElement(nat.NewNATBackendValue(net.IPv4(10, 2, 0, 1), 1111)))
			Expect(eps.m).To(ContainElement(nat.NewNATBackendValue(net.IPv4(10, 2, 0, 1), 2222)))
		}))

		By("deletng the test-service", makestep(func() {
			delete(state.SvcMap, svcKey)
			delete(state.EpsMap, svcKey)

			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			Expect(svcs.m).To(HaveLen(1))
			val, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 2), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val.Count()).To(Equal(uint32(2)))

			Expect(eps.m).To(HaveLen(2))
			Expect(eps.m).To(HaveKey(nat.NewNATBackendKey(val.ID(), 0)))
			Expect(eps.m).To(HaveKey(nat.NewNATBackendKey(val.ID(), 1)))
			Expect(eps.m).To(ContainElement(nat.NewNATBackendValue(net.IPv4(10, 2, 0, 1), 1111)))
			Expect(eps.m).To(ContainElement(nat.NewNATBackendValue(net.IPv4(10, 2, 0, 1), 2222)))
		}))

		By("deleting one second-service backend", makestep(func() {
			state.EpsMap[svcKey2] = []k8sp.Endpoint{
				&k8sp.BaseEndpointInfo{Endpoint: "10.2.0.1:2222"},
			}

			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			Expect(svcs.m).To(HaveLen(1))
			val, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 2), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val.Count()).To(Equal(uint32(1)))

			Expect(eps.m).To(HaveLen(1))
			Expect(eps.m).To(HaveKey(nat.NewNATBackendKey(val.ID(), 0)))
			Expect(eps.m).To(ContainElement(nat.NewNATBackendValue(net.IPv4(10, 2, 0, 1), 2222)))
		}))

		By("not programming eps without a service - non reachables", makestep(func() {
			nosvcKey := k8sp.ServicePortName{
				NamespacedName: types.NamespacedName{
					Namespace: "default",
					Name:      "noservice",
				},
			}

			state.EpsMap[nosvcKey] = []k8sp.Endpoint{
				&k8sp.BaseEndpointInfo{Endpoint: "10.2.0.1:6666"},
			}

			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			Expect(svcs.m).To(HaveLen(1))
			val, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 2), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val.Count()).To(Equal(uint32(1)))

			Expect(eps.m).To(HaveLen(1))
			Expect(eps.m).To(HaveKey(nat.NewNATBackendKey(val.ID(), 0)))
			Expect(eps.m).To(ContainElement(nat.NewNATBackendValue(net.IPv4(10, 2, 0, 1), 2222)))

			delete(state.EpsMap, nosvcKey)
		}))

		By("adding ExternalIP for existing service", makestep(func() {
			state.SvcMap[svcKey2] = &k8sp.BaseServiceInfo{
				ClusterIP:   net.IPv4(10, 0, 0, 2),
				Port:        2222,
				Protocol:    v1.ProtocolTCP,
				ExternalIPs: []string{"35.0.0.2"},
			}

			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			Expect(svcs.m).To(HaveLen(2))

			val1, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 2), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val1.Count()).To(Equal(uint32(1)))

			val2, ok := svcs.m[nat.NewNATKey(net.IPv4(35, 0, 0, 2), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val1).To(Equal(val2))

			Expect(eps.m).To(HaveLen(1))
			Expect(eps.m).To(HaveKey(nat.NewNATBackendKey(val1.ID(), 0)))
			Expect(eps.m).To(ContainElement(nat.NewNATBackendValue(net.IPv4(10, 2, 0, 1), 2222)))
		}))

		By("removing ExternalIP for existing service", makestep(func() {
			state.SvcMap[svcKey2] = &k8sp.BaseServiceInfo{
				ClusterIP: net.IPv4(10, 0, 0, 2),
				Port:      2222,
				Protocol:  v1.ProtocolTCP,
			}

			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			Expect(svcs.m).To(HaveLen(1))

			val, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 2), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val.Count()).To(Equal(uint32(1)))

			Expect(eps.m).To(HaveLen(1))
			Expect(eps.m).To(HaveKey(nat.NewNATBackendKey(val.ID(), 0)))
			Expect(eps.m).To(ContainElement(nat.NewNATBackendValue(net.IPv4(10, 2, 0, 1), 2222)))
		}))

		var checkAfterResync func()

		By("turning existing service into a NodePort", makestep(func() {
			state.SvcMap[svcKey2] = &k8sp.BaseServiceInfo{
				ClusterIP: net.IPv4(10, 0, 0, 2),
				Port:      2222,
				NodePort:  2222,
				Protocol:  v1.ProtocolTCP,
			}

			checkAfterResync = func() {
				err := s.Apply(state)
				Expect(err).NotTo(HaveOccurred())

				Expect(svcs.m).To(HaveLen(3))

				val1, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 2), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
				Expect(ok).To(BeTrue())
				Expect(val1.Count()).To(Equal(uint32(1)))

				val2, ok := svcs.m[nat.NewNATKey(net.IPv4(192, 168, 0, 1), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
				Expect(ok).To(BeTrue())
				Expect(val1).To(Equal(val2))

				val3, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 123, 0, 1), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
				Expect(ok).To(BeTrue())
				Expect(val1).To(Equal(val3))

				Expect(eps.m).To(HaveLen(1))
				Expect(eps.m).To(HaveKey(nat.NewNATBackendKey(val1.ID(), 0)))
				Expect(eps.m).To(ContainElement(nat.NewNATBackendValue(net.IPv4(10, 2, 0, 1), 2222)))
			}

			checkAfterResync()
		}))

		By("resyncing after creating a new syncer with the same result", makestep(func() {
			s, _ = proxy.NewSyncer(nodeIPs, svcs, eps, rt)
			checkAfterResync()
		}))

		By("resyncing after creating a new syncer and delete stale entries", makestep(func() {
			svcs.m[nat.NewNATKey(net.IPv4(5, 5, 5, 5), 1111, 6)] = nat.NewNATValue(0xdeadbeef, 2, 2)
			eps.m[nat.NewNATBackendKey(0xdeadbeef, 0)] = nat.NewNATBackendValue(net.IPv4(6, 6, 6, 6), 666)
			eps.m[nat.NewNATBackendKey(0xdeadbeef, 1)] = nat.NewNATBackendValue(net.IPv4(7, 7, 7, 7), 777)
			s, _ = proxy.NewSyncer(nodeIPs, svcs, eps, rt)
			checkAfterResync()
		}))

		svcKey3 := k8sp.ServicePortName{
			NamespacedName: types.NamespacedName{
				Namespace: "default",
				Name:      "third-service",
			},
		}

		By("inserting another service after resync", makestep(func() {
			state.SvcMap[svcKey3] = &k8sp.BaseServiceInfo{
				ClusterIP: net.IPv4(10, 0, 0, 3),
				Port:      3333,
				NodePort:  3232,
				Protocol:  v1.ProtocolUDP,
			}
			state.EpsMap[svcKey3] = []k8sp.Endpoint{
				&k8sp.BaseEndpointInfo{Endpoint: "10.3.0.1:3434"},
			}

			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			Expect(svcs.m).To(HaveLen(6))
			Expect(eps.m).To(HaveLen(2))

			val1, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 2), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val1.Count()).To(Equal(uint32(1)))

			val2, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 3), 3333, proxy.ProtoV1ToIntPanic(v1.ProtocolUDP))]
			Expect(ok).To(BeTrue())
			Expect(val2.ID()).To(Equal(val1.ID()+1), "wrongly recycled svc ID?")

			val3, ok := svcs.m[nat.NewNATKey(net.IPv4(192, 168, 0, 1), 3232, proxy.ProtoV1ToIntPanic(v1.ProtocolUDP))]
			Expect(ok).To(BeTrue())
			Expect(val3).To(Equal(val2))

			val4, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 123, 0, 1), 3232, proxy.ProtoV1ToIntPanic(v1.ProtocolUDP))]
			Expect(ok).To(BeTrue())
			Expect(val4).To(Equal(val2))
		}))

		By("updating a port of a service", makestep(func() {
			state.SvcMap[svcKey3] = &k8sp.BaseServiceInfo{
				ClusterIP: net.IPv4(10, 0, 0, 3),
				Port:      3355,
				NodePort:  3232,
				Protocol:  v1.ProtocolUDP,
			}
			state.EpsMap[svcKey3] = []k8sp.Endpoint{
				&k8sp.BaseEndpointInfo{Endpoint: "10.3.0.1:3434"},
			}

			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			Expect(svcs.m).To(HaveLen(6))
			Expect(eps.m).To(HaveLen(2))

			Expect(svcs.m).NotTo(HaveKey(
				nat.NewNATKey(net.IPv4(10, 0, 0, 3), 3333, proxy.ProtoV1ToIntPanic(v1.ProtocolUDP))))

			val2, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 3), 3355, proxy.ProtoV1ToIntPanic(v1.ProtocolUDP))]
			Expect(ok).To(BeTrue())

			val3, ok := svcs.m[nat.NewNATKey(net.IPv4(192, 168, 0, 1), 3232, proxy.ProtoV1ToIntPanic(v1.ProtocolUDP))]
			Expect(ok).To(BeTrue())
			Expect(val3).To(Equal(val2))

			val4, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 123, 0, 1), 3232, proxy.ProtoV1ToIntPanic(v1.ProtocolUDP))]
			Expect(ok).To(BeTrue())
			Expect(val4).To(Equal(val2))
		}))

		By("updating a NodePort of a service", makestep(func() {
			state.SvcMap[svcKey3] = &k8sp.BaseServiceInfo{
				ClusterIP: net.IPv4(10, 0, 0, 3),
				Port:      3355,
				NodePort:  1212,
				Protocol:  v1.ProtocolUDP,
			}
			state.EpsMap[svcKey3] = []k8sp.Endpoint{
				&k8sp.BaseEndpointInfo{Endpoint: "10.3.0.1:3434"},
			}

			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			Expect(svcs.m).To(HaveLen(6))
			Expect(eps.m).To(HaveLen(2))

			Expect(svcs.m).NotTo(HaveKey(
				nat.NewNATKey(net.IPv4(10, 0, 0, 3), 3333, proxy.ProtoV1ToIntPanic(v1.ProtocolUDP))))

			val2, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 3), 3355, proxy.ProtoV1ToIntPanic(v1.ProtocolUDP))]
			Expect(ok).To(BeTrue())

			val3, ok := svcs.m[nat.NewNATKey(net.IPv4(192, 168, 0, 1), 1212, proxy.ProtoV1ToIntPanic(v1.ProtocolUDP))]
			Expect(ok).To(BeTrue())
			Expect(val3).To(Equal(val2))

			val4, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 123, 0, 1), 1212, proxy.ProtoV1ToIntPanic(v1.ProtocolUDP))]
			Expect(ok).To(BeTrue())
			Expect(val4).To(Equal(val2))
		}))

		By("deleting backends if there are none for a service BPF-147", makestep(func() {
			val, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 2), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			count := val.Count()
			for i := uint32(0); i < count; i++ {
				Expect(eps.m).To(HaveKey(nat.NewNATBackendKey(val.ID(), i)))
			}

			// This testcase assumes there are at least as many backends in the
			// EpsMap for other services left than the original number of services
			// for the one being updated.`
			delete(state.EpsMap, svcKey2)
			Expect(int(count)).To(BeNumerically(">=", func() int {
				cnt := 0
				for _, v := range state.EpsMap {
					cnt += len(v)
				}
				return cnt
			}()))

			log("state.SvcMap = %+v\n", state.SvcMap)
			log("state.EpsMap = %+v\n", state.EpsMap)
			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			val, ok = svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 2), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val.Count()).To(Equal(uint32(0)))
			for i := uint32(0); i < count; i++ {
				Expect(eps.m).NotTo(HaveKey(nat.NewNATBackendKey(val.ID(), i)))
			}
		}))

		By("deleting the services", makestep(func() {
			delete(state.SvcMap, svcKey2)
			delete(state.SvcMap, svcKey3)

			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			Expect(svcs.m).To(HaveLen(0))
			Expect(eps.m).To(HaveLen(0))
		}))

		By("inserting only non-local ep for a NodePort - no route", makestep(func() {
			// use the meta node IP for nodeports as well
			s, _ = proxy.NewSyncer(append(nodeIPs, net.IPv4(255, 255, 255, 255)), svcs, eps, rt)
			state.SvcMap[svcKey2] = &k8sp.BaseServiceInfo{
				ClusterIP:              net.IPv4(10, 0, 0, 2),
				Port:                   2222,
				NodePort:               4444,
				Protocol:               v1.ProtocolTCP,
				OnlyNodeLocalEndpoints: true,
			}

			state.EpsMap[svcKey2] = []k8sp.Endpoint{
				&k8sp.BaseEndpointInfo{Endpoint: "10.2.1.1:2222"},
			}

			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			Expect(svcs.m).To(HaveLen(3))
			Expect(eps.m).To(HaveLen(1))
		}))

		By("inserting only non-local ep for a NodePort - with route", makestep(func() {
			// use the meta node IP for nodeports as well
			s, _ = proxy.NewSyncer(append(nodeIPs, net.IPv4(255, 255, 255, 255)), svcs, eps, rt)
			state.SvcMap[svcKey2] = &k8sp.BaseServiceInfo{
				ClusterIP:              net.IPv4(10, 0, 0, 2),
				Port:                   2222,
				NodePort:               4444,
				Protocol:               v1.ProtocolTCP,
				OnlyNodeLocalEndpoints: true,
			}

			state.EpsMap[svcKey2] = []k8sp.Endpoint{
				&k8sp.BaseEndpointInfo{Endpoint: "10.2.1.1:2222"},
			}

			_ = rt.Update(
				routes.NewKey(ip.CIDRFromAddrAndPrefix(ip.FromString("10.2.1.0"), 24).(ip.V4CIDR)),
				routes.NewValueWithNextHop(
					routes.TypeRemoteWorkload,
					ip.FromString("10.123.0.111").(ip.V4Addr)),
			)

			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			Expect(svcs.m).To(HaveLen(4))
			Expect(eps.m).To(HaveLen(2))
		}))

		By("inserting only non-local eps for a NodePort - multiple nodes & pods/node", makestep(func() {
			// use the meta node IP for nodeports as well
			s, _ = proxy.NewSyncer(append(nodeIPs, net.IPv4(255, 255, 255, 255)), svcs, eps, rt)
			state.SvcMap[svcKey2] = &k8sp.BaseServiceInfo{
				ClusterIP:              net.IPv4(10, 0, 0, 2),
				Port:                   2222,
				NodePort:               4444,
				Protocol:               v1.ProtocolTCP,
				OnlyNodeLocalEndpoints: true,
			}

			state.EpsMap[svcKey2] = []k8sp.Endpoint{
				&k8sp.BaseEndpointInfo{Endpoint: "10.2.1.1:2222"},
				&k8sp.BaseEndpointInfo{Endpoint: "10.2.2.1:2222"},
				&k8sp.BaseEndpointInfo{Endpoint: "10.2.2.2:2222"},
				&k8sp.BaseEndpointInfo{Endpoint: "10.2.3.1:2222"},
			}

			_ = rt.Update(
				routes.NewKey(ip.CIDRFromAddrAndPrefix(ip.FromString("10.2.2.0"), 24).(ip.V4CIDR)),
				routes.NewValueWithNextHop(
					routes.TypeRemoteWorkload,
					ip.FromString("10.123.0.112").(ip.V4Addr)),
			)
			_ = rt.Update(
				routes.NewKey(ip.CIDRFromAddrAndPrefix(ip.FromString("10.2.3.0"), 24).(ip.V4CIDR)),
				routes.NewValueWithNextHop(
					routes.TypeRemoteWorkload,
					ip.FromString("10.123.0.113").(ip.V4Addr)),
			)

			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			checkAfterResync = func() {
				Expect(svcs.m).To(HaveLen(6))

				val1, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 2), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
				Expect(ok).To(BeTrue())
				Expect(val1.Count()).To(Equal(uint32(4)))

				val2, ok := svcs.m[nat.NewNATKey(net.IPv4(192, 168, 0, 1), 4444, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
				Expect(ok).To(BeTrue())
				Expect(val2.ID()).To(Equal(val1.ID()))
				Expect(val2.Count()).To(Equal(uint32(0)))

				val3, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 123, 0, 1), 4444, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
				Expect(ok).To(BeTrue())
				Expect(val2).To(Equal(val3))

				Expect(eps.m).To(HaveLen(8))

				all := make([]nat.BackendValue, 0, 4)
				for i := uint32(0); i < val1.Count(); i++ {
					bk := nat.NewNATBackendKey(val1.ID(), i)
					Expect(eps.m).To(HaveKey(bk))
					all = append(all, eps.m[bk])
				}

				Expect(all).To(ContainElement(nat.NewNATBackendValue(net.IPv4(10, 2, 1, 1), 2222)))
				Expect(all).To(ContainElement(nat.NewNATBackendValue(net.IPv4(10, 2, 2, 1), 2222)))
				Expect(all).To(ContainElement(nat.NewNATBackendValue(net.IPv4(10, 2, 2, 2), 2222)))
				Expect(all).To(ContainElement(nat.NewNATBackendValue(net.IPv4(10, 2, 3, 1), 2222)))

				checkRemote := func(a net.IP, count uint32) {
					k := nat.NewNATKey(a, 4444, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))
					Expect(svcs.m).To(HaveKey(k))
					v := svcs.m[k]
					Expect(v.Count()).To(Equal(count))
				}

				checkRemote(net.IPv4(10, 123, 0, 111), 1)
				checkRemote(net.IPv4(10, 123, 0, 112), 2)
				checkRemote(net.IPv4(10, 123, 0, 113), 1)
			}

			checkAfterResync()
		}))

		By("restarting Syncer to check if NodePortRemotes are picked up correctly", makestep(func() {
			// use the meta node IP for nodeports as well
			s, _ = proxy.NewSyncer(append(nodeIPs, net.IPv4(255, 255, 255, 255)), svcs, eps, rt)
			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			checkAfterResync()
		}))

		By("inserting a local ep for a NodePort", makestep(func() {
			state.SvcMap[svcKey2] = &k8sp.BaseServiceInfo{
				ClusterIP:              net.IPv4(10, 0, 0, 2),
				Port:                   2222,
				NodePort:               4444,
				Protocol:               v1.ProtocolTCP,
				OnlyNodeLocalEndpoints: true,
			}

			state.EpsMap[svcKey2] = []k8sp.Endpoint{
				&k8sp.BaseEndpointInfo{Endpoint: "10.2.0.1:2222"},
				&k8sp.BaseEndpointInfo{Endpoint: "10.3.0.1:2222", IsLocal: true},
				&k8sp.BaseEndpointInfo{Endpoint: "10.4.0.1:2222"},
				&k8sp.BaseEndpointInfo{Endpoint: "10.5.0.1:2222", IsLocal: true},
			}

			err := s.Apply(state)
			Expect(err).NotTo(HaveOccurred())

			Expect(svcs.m).To(HaveLen(4))

			val1, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 0, 0, 2), 2222, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val1.Count()).To(Equal(uint32(4)))
			Expect(val1.LocalCount()).To(Equal(uint32(2)))

			val2, ok := svcs.m[nat.NewNATKey(net.IPv4(192, 168, 0, 1), 4444, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val2.ID()).To(Equal(val1.ID()))
			Expect(val2.Count()).To(Equal(uint32(2)))

			val3, ok := svcs.m[nat.NewNATKey(net.IPv4(10, 123, 0, 1), 4444, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val2).To(Equal(val3))

			val4, ok := svcs.m[nat.NewNATKey(net.IPv4(255, 255, 255, 255), 4444, proxy.ProtoV1ToIntPanic(v1.ProtocolTCP))]
			Expect(ok).To(BeTrue())
			Expect(val2).To(Equal(val4))

			Expect(eps.m).To(HaveLen(4))

			Expect(eps.m).To(HaveKey(nat.NewNATBackendKey(val1.ID(), 0)))
			Expect(eps.m).To(HaveKey(nat.NewNATBackendKey(val1.ID(), 1)))
			Expect(eps.m).To(HaveKey(nat.NewNATBackendKey(val1.ID(), 2)))
			Expect(eps.m).To(HaveKey(nat.NewNATBackendKey(val1.ID(), 3)))

			Expect(eps.m[nat.NewNATBackendKey(val1.ID(), 0)]).To(Or(
				Equal(nat.NewNATBackendValue(net.IPv4(10, 3, 0, 1), 2222)),
				Equal(nat.NewNATBackendValue(net.IPv4(10, 5, 0, 1), 2222))))
			Expect(eps.m[nat.NewNATBackendKey(val1.ID(), 1)]).To(Or(
				Equal(nat.NewNATBackendValue(net.IPv4(10, 3, 0, 1), 2222)),
				Equal(nat.NewNATBackendValue(net.IPv4(10, 5, 0, 1), 2222))))
			Expect(eps.m[nat.NewNATBackendKey(val1.ID(), 0)]).
				NotTo(Equal(eps.m[nat.NewNATBackendKey(val1.ID(), 1)]))

			Expect(eps.m[nat.NewNATBackendKey(val1.ID(), 2)]).To(Or(
				Equal(nat.NewNATBackendValue(net.IPv4(10, 2, 0, 1), 2222)),
				Equal(nat.NewNATBackendValue(net.IPv4(10, 4, 0, 1), 2222))))
			Expect(eps.m[nat.NewNATBackendKey(val1.ID(), 3)]).To(Or(
				Equal(nat.NewNATBackendValue(net.IPv4(10, 2, 0, 1), 2222)),
				Equal(nat.NewNATBackendValue(net.IPv4(10, 4, 0, 1), 2222))))
			Expect(eps.m[nat.NewNATBackendKey(val1.ID(), 2)]).
				NotTo(Equal(eps.m[nat.NewNATBackendKey(val1.ID(), 3)]))
		}))
	})
})

type mockNATMap struct {
	sync.Mutex
	m map[nat.FrontendKey]nat.FrontendValue
}

func (m *mockNATMap) MapFD() bpf.MapFD {
	panic("implement me")
}

func newMockNATMap() *mockNATMap {
	return &mockNATMap{
		m: make(map[nat.FrontendKey]nat.FrontendValue),
	}
}

func (m *mockNATMap) EnsureExists() error {
	return nil
}

func (m *mockNATMap) GetName() string {
	return "nat"
}

func (m *mockNATMap) Path() string {
	return "/sys/fs/bpf/tc/nat"
}

func (m *mockNATMap) Iter(iter bpf.MapIter) error {
	m.Lock()
	defer m.Unlock()

	ks := len(nat.FrontendKey{})
	vs := len(nat.FrontendValue{})
	for k, v := range m.m {
		iter(k[:ks], v[:vs])
	}

	return nil
}

func (m *mockNATMap) Update(k, v []byte) error {
	m.Lock()
	defer m.Unlock()

	ks := len(nat.FrontendKey{})
	if len(k) != ks {
		return errors.Errorf("expected key size %d got %d", ks, len(k))
	}
	vs := len(nat.FrontendValue{})
	if len(v) != vs {
		return errors.Errorf("expected value size %d got %d", vs, len(k))
	}

	var key nat.FrontendKey
	copy(key[:ks], k[:ks])

	var val nat.FrontendValue
	copy(val[:vs], v[:vs])

	m.m[key] = val

	return nil
}

func (m *mockNATMap) Get(k []byte) ([]byte, error) {
	panic("not implemented")
}

func (m *mockNATMap) Delete(k []byte) error {
	m.Lock()
	defer m.Unlock()

	ks := len(nat.FrontendKey{})
	if len(k) != ks {
		return errors.Errorf("expected key size %d got %d", ks, len(k))
	}

	var key nat.FrontendKey
	copy(key[:ks], k[:ks])

	delete(m.m, key)

	return nil
}

type mockNATBackendMap struct {
	sync.Mutex
	m map[nat.BackendKey]nat.BackendValue
}

func (m *mockNATBackendMap) MapFD() bpf.MapFD {
	panic("implement me")
}

func newMockNATBackendMap() *mockNATBackendMap {
	return &mockNATBackendMap{
		m: make(map[nat.BackendKey]nat.BackendValue),
	}
}

func (m *mockNATBackendMap) EnsureExists() error {
	return nil
}

func (m *mockNATBackendMap) GetName() string {
	return "natbe"
}

func (m *mockNATBackendMap) Path() string {
	return "/sys/fs/bpf/tc/natbe"
}

func (m *mockNATBackendMap) Iter(iter bpf.MapIter) error {
	m.Lock()
	defer m.Unlock()

	ks := len(nat.BackendKey{})
	vs := len(nat.BackendValue{})
	for k, v := range m.m {
		iter(k[:ks], v[:vs])
	}

	return nil
}

func (m *mockNATBackendMap) Update(k, v []byte) error {
	m.Lock()
	defer m.Unlock()

	ks := len(nat.BackendKey{})
	if len(k) != ks {
		return errors.Errorf("expected key size %d got %d", ks, len(k))
	}
	vs := len(nat.BackendValue{})
	if len(v) != vs {
		return errors.Errorf("expected value size %d got %d", vs, len(k))
	}

	var key nat.BackendKey
	copy(key[:ks], k[:ks])

	var val nat.BackendValue
	copy(val[:vs], v[:vs])

	m.m[key] = val

	return nil
}

func (m *mockNATBackendMap) Get(k []byte) ([]byte, error) {
	panic("not implemented")
}

func (m *mockNATBackendMap) Delete(k []byte) error {
	m.Lock()
	defer m.Unlock()

	ks := len(nat.BackendKey{})
	if len(k) != ks {
		return errors.Errorf("expected key size %d got %d", ks, len(k))
	}

	var key nat.BackendKey
	copy(key[:ks], k[:ks])

	delete(m.m, key)

	return nil
}
