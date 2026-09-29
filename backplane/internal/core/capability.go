// Package core holds the provider-neutral model shared by every part of
// Backplane: capabilities, blueprints, manifests, health and runs.
package core

// Capability is a provider-neutral job a backend needs done. Providers
// implement capabilities; templates and plans are written in terms of them so
// no part of the system is hard-wired to one company.
type Capability string

const (
	CapDatabase     Capability = "database"
	CapAuth         Capability = "auth"
	CapStorage      Capability = "storage"
	CapPayments     Capability = "payments"
	CapEmail        Capability = "email"
	CapSMS          Capability = "sms"
	CapCompute      Capability = "compute"
	CapFunctions    Capability = "functions"
	CapQueue        Capability = "queue"
	CapCache        Capability = "cache"
	CapRealtime     Capability = "realtime"
	CapVector       Capability = "vector"
	CapAnalytics    Capability = "analytics"
	CapMonitoring   Capability = "monitoring"
	CapDeployment   Capability = "deployment"
	CapDNS          Capability = "dns"
	CapSecrets      Capability = "secrets"
	CapWebhooks     Capability = "webhooks"
	CapCICD         Capability = "cicd"
	CapScheduler    Capability = "scheduler"
	CapJobs         Capability = "jobs"
	CapPush         Capability = "push"
	CapFeatureFlags Capability = "feature_flags"
	CapMedia        Capability = "media"
	CapAPI          Capability = "api"
)

// CapabilityInfo explains a capability in plain English for the guided builder.
type CapabilityInfo struct {
	ID      Capability `json:"id"`
	Label   string     `json:"label"`
	Plain   string     `json:"plain"`   // one-line plain-English description
	Example string     `json:"example"` // "Customers get a receipt after paying"
	Group   string     `json:"group"`   // data, money, messaging, runtime, operations
}

// Capabilities is the ordered catalogue shown in the guided builder.
var Capabilities = []CapabilityInfo{
	{CapDatabase, "Database", "Stores your records — customers, orders, bookings.", "Keep a list of every order", "data"},
	{CapAuth, "Accounts & sign-in", "Lets people create accounts and sign in safely.", "Customers log in to see purchases", "data"},
	{CapStorage, "File storage", "Holds files such as downloads, uploads and images.", "Store the installer customers buy", "data"},
	{CapPayments, "Payments", "Takes one-time payments, deposits or subscriptions.", "Charge $29 once", "money"},
	{CapEmail, "Email", "Sends receipts, links, reminders and sign-in emails.", "Email a download link after payment", "messaging"},
	{CapSMS, "Text messages", "Sends SMS codes and reminders.", "Text a reminder the day before", "messaging"},
	{CapAPI, "API", "A web address your app or site talks to.", "Your site's Buy button calls it", "runtime"},
	{CapFunctions, "Serverless functions", "Small pieces of code that run on demand.", "Handle a payment notification", "runtime"},
	{CapRealtime, "Realtime", "Pushes live updates to open screens.", "A chat message appears instantly", "runtime"},
	{CapJobs, "Background jobs", "Work that happens after the customer has moved on.", "Resize an upload later", "runtime"},
	{CapQueue, "Queues", "A waiting line so busy moments don't drop work.", "Send 500 emails without timeouts", "runtime"},
	{CapScheduler, "Scheduled tasks", "Runs something on a timetable.", "Send reminders every morning", "runtime"},
	{CapWebhooks, "Webhooks", "Receives notifications from other services.", "Know the moment a payment clears", "runtime"},
	{CapPush, "Push notifications", "Notifies phones and browsers.", "Tell a customer their order shipped", "messaging"},
	{CapAnalytics, "Analytics", "Counts what people do in your product.", "See how many people reach checkout", "operations"},
	{CapMonitoring, "Error monitoring", "Catches crashes and errors with details.", "Get told when checkout breaks", "operations"},
	{CapVector, "AI / vector search", "Finds similar text or images for AI features.", "Answer questions from your docs", "data"},
	{CapMedia, "Media processing", "Resizes, converts and optimizes images and video.", "Make thumbnails from uploads", "data"},
	{CapDeployment, "Deployment", "Puts your code online.", "Publish a new version safely", "operations"},
	{CapDNS, "Domains & DNS", "Connects your domain name to your services.", "api.yourbusiness.com", "operations"},
	{CapCICD, "CI/CD", "Tests and deploys automatically when code changes.", "Deploy when you push to main", "operations"},
	{CapSecrets, "Secrets", "Keeps keys and passwords out of your code.", "Store the payment key safely", "operations"},
	{CapFeatureFlags, "Feature flags", "Turns features on or off without redeploying.", "Try a new checkout with 10% of people", "operations"},
	{CapCache, "Cache", "Remembers answers so things load faster.", "Keep product info close to visitors", "data"},
	{CapCompute, "Compute", "Always-on or on-demand servers.", "Run a small API", "runtime"},
}

// CapabilityByID finds a capability's info.
func CapabilityByID(id Capability) (CapabilityInfo, bool) {
	for _, c := range Capabilities {
		if c.ID == id {
			return c, true
		}
	}
	return CapabilityInfo{}, false
}
