package check

import (
	"testing"

	routev1 "github.com/openshift/api/route/v1"
)

func TestGetAdmittedHost(t *testing.T) {
	tests := []struct {
		name  string
		route *routev1.Route
		want  string
	}{
		{
			"admitted ingress",
			&routev1.Route{
				Status: routev1.RouteStatus{
					Ingress: []routev1.RouteIngress{
						{
							Host: "s3.apps.cluster.example.com",
							Conditions: []routev1.RouteIngressCondition{
								{Type: routev1.RouteAdmitted, Status: "True"},
							},
						},
					},
				},
			},
			"s3.apps.cluster.example.com",
		},
		{
			"not admitted falls back to first ingress",
			&routev1.Route{
				Status: routev1.RouteStatus{
					Ingress: []routev1.RouteIngress{
						{
							Host: "s3.apps.cluster.example.com",
							Conditions: []routev1.RouteIngressCondition{
								{Type: routev1.RouteAdmitted, Status: "False"},
							},
						},
					},
				},
			},
			"s3.apps.cluster.example.com",
		},
		{
			"no ingress falls back to spec host",
			&routev1.Route{
				Spec: routev1.RouteSpec{Host: "s3.spec.example.com"},
			},
			"s3.spec.example.com",
		},
		{
			"multiple ingresses second admitted",
			&routev1.Route{
				Status: routev1.RouteStatus{
					Ingress: []routev1.RouteIngress{
						{
							Host: "old.example.com",
							Conditions: []routev1.RouteIngressCondition{
								{Type: routev1.RouteAdmitted, Status: "False"},
							},
						},
						{
							Host: "new.example.com",
							Conditions: []routev1.RouteIngressCondition{
								{Type: routev1.RouteAdmitted, Status: "True"},
							},
						},
					},
				},
			},
			"new.example.com",
		},
		{
			"empty route",
			&routev1.Route{},
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := getAdmittedHost(tt.route)
			if got != tt.want {
				t.Errorf("getAdmittedHost() = %q, want %q", got, tt.want)
			}
		})
	}
}
