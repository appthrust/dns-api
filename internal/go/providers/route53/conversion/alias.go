package conversion

import "strings"

var route53CanonicalHostedZones = map[string]string{
	// Application Load Balancers and Classic Load Balancers.
	"us-east-2.elb.amazonaws.com":         "Z3AADJGX6KTTL2",
	"us-east-1.elb.amazonaws.com":         "Z35SXDOTRQ7X7K",
	"us-west-1.elb.amazonaws.com":         "Z368ELLRRE2KJ0",
	"us-west-2.elb.amazonaws.com":         "Z1H1FL5HABSF5",
	"ca-central-1.elb.amazonaws.com":      "ZQSVJUPU6J1EY",
	"ca-west-1.elb.amazonaws.com":         "Z06473681N0SF6OS049SD",
	"ap-east-1.elb.amazonaws.com":         "Z3DQVH9N71FHZ0",
	"ap-east-2.elb.amazonaws.com":         "Z02789141MW7T1WBU19PO",
	"ap-south-1.elb.amazonaws.com":        "ZP97RAFLXTNZK",
	"ap-south-2.elb.amazonaws.com":        "Z0173938T07WNTVAEPZN",
	"ap-northeast-1.elb.amazonaws.com":    "Z14GRHDCWA56QT",
	"ap-northeast-2.elb.amazonaws.com":    "ZWKZPGTI48KDX",
	"ap-northeast-3.elb.amazonaws.com":    "Z5LXEXXYW11ES",
	"ap-southeast-1.elb.amazonaws.com":    "Z1LMS91P8CMLE5",
	"ap-southeast-2.elb.amazonaws.com":    "Z1GM3OXH4ZPM65",
	"ap-southeast-3.elb.amazonaws.com":    "Z08888821HLRG5A9ZRTER",
	"ap-southeast-4.elb.amazonaws.com":    "Z09517862IB2WZLPXG76F",
	"ap-southeast-5.elb.amazonaws.com":    "Z06010284QMVVW7WO5J",
	"ap-southeast-6.elb.amazonaws.com":    "Z023301818UFJ50CIO0MV",
	"ap-southeast-7.elb.amazonaws.com":    "Z0390008CMBRTHFGWBCB",
	"eu-central-1.elb.amazonaws.com":      "Z215JYRZR1TBD5",
	"eu-central-2.elb.amazonaws.com":      "Z06391101F2ZOEP8P5EB3",
	"eu-west-1.elb.amazonaws.com":         "Z32O12XQLNTSW2",
	"eu-west-2.elb.amazonaws.com":         "ZHURV8PSTC4K8",
	"eu-west-3.elb.amazonaws.com":         "Z3Q77PNBQS71R4",
	"eu-north-1.elb.amazonaws.com":        "Z23TAZ6LKFMNIO",
	"eu-south-1.elb.amazonaws.com":        "Z3ULH7SSC9OV64",
	"eu-south-2.elb.amazonaws.com":        "Z0956581394HF5D5LXGAP",
	"sa-east-1.elb.amazonaws.com":         "Z2P70J7HTTTPLU",
	"cn-north-1.elb.amazonaws.com.cn":     "Z1GDH35T77C1KE",
	"cn-northwest-1.elb.amazonaws.com.cn": "ZM7IZAIOVVDZF",
	"us-gov-west-1.elb.amazonaws.com":     "Z33AYJ8TM3BH4J",
	"us-gov-east-1.elb.amazonaws.com":     "Z166TLBEWOO7G0",
	"mx-central-1.elb.amazonaws.com":      "Z023552324OKD1BB28BH5",
	"me-central-1.elb.amazonaws.com":      "Z08230872XQRWHG2XF6I",
	"me-south-1.elb.amazonaws.com":        "ZS929ML54UICD",
	"af-south-1.elb.amazonaws.com":        "Z268VQBMOI5EKX",
	"il-central-1.elb.amazonaws.com":      "Z09170902867EHPV2DABU",

	// Network Load Balancers.
	"elb.us-east-2.amazonaws.com":         "ZLMOA37VPKANP",
	"elb.us-east-1.amazonaws.com":         "Z26RNL4JYFTOTI",
	"elb.us-west-1.amazonaws.com":         "Z24FKFUX50B4VW",
	"elb.us-west-2.amazonaws.com":         "Z18D5FSROUN65G",
	"elb.ca-central-1.amazonaws.com":      "Z2EPGBW3API2WT",
	"elb.ca-west-1.amazonaws.com":         "Z02754302KBB00W2LKWZ9",
	"elb.ap-east-1.amazonaws.com":         "Z12Y7K3UBGUAD1",
	"elb.ap-east-2.amazonaws.com":         "Z09176273OC2HWIAUNYW",
	"elb.ap-south-1.amazonaws.com":        "ZVDDRBQ08TROA",
	"elb.ap-south-2.amazonaws.com":        "Z0711778386UTO08407HT",
	"elb.ap-northeast-1.amazonaws.com":    "Z31USIVHYNEOWT",
	"elb.ap-northeast-2.amazonaws.com":    "ZIBE1TIR4HY56",
	"elb.ap-northeast-3.amazonaws.com":    "Z1GWIQ4HH19I5X",
	"elb.ap-southeast-1.amazonaws.com":    "ZKVM4W9LS7TM",
	"elb.ap-southeast-2.amazonaws.com":    "ZCT6FZBF4DROD",
	"elb.ap-southeast-3.amazonaws.com":    "Z01971771FYVNCOVWJU1G",
	"elb.ap-southeast-4.amazonaws.com":    "Z01156963G8MIIL7X90IV",
	"elb.ap-southeast-5.amazonaws.com":    "Z026317210H9ACVTRO6FB",
	"elb.ap-southeast-6.amazonaws.com":    "Z01392953RKV2Q3RBP0KU",
	"elb.ap-southeast-7.amazonaws.com":    "Z054363131YWATEMWRG5L",
	"elb.eu-central-1.amazonaws.com":      "Z3F0SRJ5LGBH90",
	"elb.eu-central-2.amazonaws.com":      "Z02239872DOALSIDCX66S",
	"elb.eu-west-1.amazonaws.com":         "Z2IFOLAFXWLO4F",
	"elb.eu-west-2.amazonaws.com":         "ZD4D7Y8KGAS4G",
	"elb.eu-west-3.amazonaws.com":         "Z1CMS0P5QUZ6D5",
	"elb.eu-north-1.amazonaws.com":        "Z1UDT6IFJ4EJM",
	"elb.eu-south-1.amazonaws.com":        "Z23146JA1KNAFP",
	"elb.eu-south-2.amazonaws.com":        "Z1011216NVTVYADP1SSV",
	"elb.sa-east-1.amazonaws.com":         "ZTK26PT1VY4CU",
	"elb.cn-north-1.amazonaws.com.cn":     "Z3QFB96KMJ7ED6",
	"elb.cn-northwest-1.amazonaws.com.cn": "ZQEIKTCZ8352D",
	"elb.us-gov-west-1.amazonaws.com":     "ZMG1MZ2THAWF1",
	"elb.us-gov-east-1.amazonaws.com":     "Z1ZSMQQ6Q24QQ8",
	"elb.mx-central-1.amazonaws.com":      "Z02031231H3ID6HYJ9A7U",
	"elb.me-central-1.amazonaws.com":      "Z00282643NTTLPANJJG2P",
	"elb.me-south-1.amazonaws.com":        "Z3QSRYVP46NYYV",
	"elb.af-south-1.amazonaws.com":        "Z203XCE67M25HM",
	"elb.il-central-1.amazonaws.com":      "Z0313266YDI6ZRHTGQY4",
}

func route53CanonicalHostedZone(hostname string) string {
	trimmed := strings.TrimSuffix(strings.ToLower(hostname), ".")
	parts := strings.Split(trimmed, ".")
	for i := len(parts) - 2; i >= 0; i-- {
		if zone, ok := route53CanonicalHostedZones[strings.Join(parts[i:], ".")]; ok {
			return zone
		}
	}
	return ""
}

func normalizeRoute53AliasDNSName(hostname string) string {
	trimmed := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	if route53CanonicalHostedZone(trimmed) != "" && !strings.HasPrefix(trimmed, "dualstack.") {
		trimmed = "dualstack." + trimmed
	}
	return trimmed + "."
}
