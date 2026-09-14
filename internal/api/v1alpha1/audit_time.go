package v1alpha1

import "time"

// AuditTime preserves fractional seconds in durable audit evidence. time.Time's
// JSON decoder also accepts the seconds-only values written by older versions.
// Unlike metav1.Time, neither JSON nor unstructured conversion truncates it.
// +kubebuilder:validation:Type=string
// +kubebuilder:validation:Format=date-time
type AuditTime struct {
	time.Time `json:"-"`
}

func NewAuditTime(t time.Time) AuditTime {
	return AuditTime{Time: t.UTC()}
}

func (t AuditTime) ToUnstructured() interface{} {
	return t.UTC().Format(time.RFC3339Nano)
}

// DeepCopyInto copies time.Time by value, including its immutable location.
func (in *AuditTime) DeepCopyInto(out *AuditTime) { *out = *in }
