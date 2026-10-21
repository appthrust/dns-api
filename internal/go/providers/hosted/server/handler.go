package server

import (
	"net"
	"time"

	"github.com/miekg/dns"
)

const udpPayloadSize = 1232

type handler struct {
	index   *zoneIndex
	limiter *sourceLimiter
	metrics *serverMetrics
}

func (h *handler) ServeDNS(writer dns.ResponseWriter, request *dns.Msg) {
	started := time.Now()
	_, udp := writer.RemoteAddr().(*net.UDPAddr)
	if address, ok := writer.RemoteAddr().(*net.UDPAddr); ok && !h.limiter.allow(address.AddrPort().Addr().Unmap(), started) {
		h.metrics.rateLimited.Inc()
		return
	}
	response, apex := h.answer(request)
	if udp {
		size := dns.MinMsgSize
		if opt := request.IsEdns0(); opt != nil {
			size = min(udpPayloadSize, max(dns.MinMsgSize, int(opt.UDPSize())))
		}
		response.Truncate(size)
	}
	_ = writer.WriteMsg(response)
	qtype := "OTHER"
	if len(request.Question) == 1 {
		if name, ok := dns.TypeToString[request.Question[0].Qtype]; ok {
			qtype = name
		}
	}
	h.metrics.queries.WithLabelValues(apex, qtype, dns.RcodeToString[response.Rcode]).Inc()
	h.metrics.duration.Observe(time.Since(started).Seconds())
}

func (h *handler) answer(request *dns.Msg) (*dns.Msg, string) {
	response := new(dns.Msg)
	response.SetReply(request)
	response.Authoritative = true
	response.RecursionAvailable = false
	response.Compress = true
	if opt := request.IsEdns0(); opt != nil {
		response.SetEdns0(udpPayloadSize, false)
		if opt.Version() != 0 {
			response.Rcode = dns.RcodeBadVers
			return response, ""
		}
	}
	if request.Opcode != dns.OpcodeQuery {
		response.Rcode = dns.RcodeNotImplemented
		return response, ""
	}
	if len(request.Question) != 1 {
		response.Rcode = dns.RcodeFormatError
		return response, ""
	}
	question := request.Question[0]
	name := dns.CanonicalName(question.Name)
	z := h.index.lookup(name)
	if z == nil || question.Qclass != dns.ClassINET {
		response.Rcode = dns.RcodeRefused
		return response, ""
	}
	if question.Qtype == dns.TypeAXFR || question.Qtype == dns.TypeIXFR {
		response.Rcode = dns.RcodeRefused
		return response, z.apex
	}
	if question.Qtype == dns.TypeANY {
		response.Answer = []dns.RR{&dns.HINFO{Hdr: dns.RR_Header{Name: question.Name, Rrtype: dns.TypeHINFO, Class: dns.ClassINET, Ttl: z.soa.Hdr.Ttl}, Cpu: "RFC8482", Os: ""}}
		return response, z.apex
	}
	ownerCase := question.Name
	var seen [8]string
	links := 0
	for {
		rrsets, exists := z.lookup(name)
		if !exists {
			response.Rcode = dns.RcodeNameError
			response.Ns = []dns.RR{z.negativeSOA}
			break
		}
		if records := rrsets[question.Qtype]; len(records) != 0 {
			response.Answer = appendWithOwner(response.Answer, records, ownerCase)
			if name == z.apex && (question.Qtype == dns.TypeSOA || question.Qtype == dns.TypeNS) {
				response.Extra = append(response.Extra, z.glue...)
			} else {
				response.Extra = appendAdditional(response.Extra, records, z)
			}
			break
		}
		cnames := rrsets[dns.TypeCNAME]
		if len(cnames) == 0 {
			response.Ns = []dns.RR{z.negativeSOA}
			break
		}
		if links == len(seen) {
			response.Answer = nil
			response.Rcode = dns.RcodeServerFailure
			break
		}
		for _, visited := range seen[:links] {
			if visited == name {
				response.Answer = nil
				response.Rcode = dns.RcodeServerFailure
				return response, z.apex
			}
		}
		seen[links] = name
		links++
		response.Answer = appendWithOwner(response.Answer, cnames, ownerCase)
		target := cnames[0].(*dns.CNAME).Target
		name = dns.CanonicalName(target)
		ownerCase = target
		// Do not recurse or chase outside this zone. A concurrent publication
		// must not replace the immutable snapshot used by this query.
		if !dns.IsSubDomain(z.apex, name) {
			break
		}
		if authority := h.index.lookup(name); authority == nil || authority.apex != z.apex {
			break
		}
	}
	return response, z.apex
}

func appendWithOwner(answer []dns.RR, records []dns.RR, owner string) []dns.RR {
	for _, record := range records {
		if record.Header().Name != owner {
			record = dns.Copy(record)
			record.Header().Name = owner
		}
		answer = append(answer, record)
	}
	return answer
}

func appendAdditional(additional []dns.RR, records []dns.RR, z *zone) []dns.RR {
	for _, record := range records {
		var target string
		switch record := record.(type) {
		case *dns.NS:
			target = record.Ns
		case *dns.MX:
			target = record.Mx
		default:
			continue
		}
		rrsets := z.owners[dns.CanonicalName(target)]
		for _, kind := range []uint16{dns.TypeA, dns.TypeAAAA} {
			for _, address := range rrsets[kind] {
				duplicate := false
				for _, present := range additional {
					if present == address {
						duplicate = true
						break
					}
				}
				if !duplicate {
					additional = append(additional, address)
				}
			}
		}
	}
	return additional
}
