package http

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"flomation.app/automate/api"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// categoryMetadata maps the first path segment of an action ID to its display metadata.
var categoryMetadata = map[string]api.ActionCategory{
	"ai":             {Key: "ai", Name: "AI", Icon: "brain", Description: "Artificial intelligence and large language model integrations"},
	"airtable":       {Key: "airtable", Name: "Airtable", Icon: "airtable", Description: "Manage bases, tables, and records in Airtable"},
	"arithmetic":     {Key: "arithmetic", Name: "Arithmetic", Icon: "calculator", Description: "Mathematical operations"},
	"aws":            {Key: "aws", Name: "AWS", Icon: "cloud", Description: "Amazon Web Services integrations"},
	"common":         {Key: "common", Name: "Common", Icon: "toolbox", Description: "General-purpose data utilities"},
	"conditional":    {Key: "conditional", Name: "Conditional", Icon: "code-branch", Description: "Control flow based on conditions"},
	"humanintheloop": {Key: "humanintheloop", Name: "Human in the Loop", Icon: "user-check", Description: "Pause a flow for a human decision, then branch on their response"},
	"file":           {Key: "file", Name: "File", Icon: "file", Description: "Read and write files"},
	"git":            {Key: "git", Name: "Git", Icon: "code-branch", Description: "Version control operations"},
	"github":         {Key: "github", Name: "GitHub", Icon: "github", Description: "Manage pull requests, workflows, and issues in GitHub"},
	"gitlab":         {Key: "gitlab", Name: "GitLab", Icon: "gitlab", Description: "Manage merge requests, pipelines, and issues in GitLab"},
	"output":         {Key: "output", Name: "Output", Icon: "location-arrow", Description: "Send data to external destinations"},
	"security":       {Key: "security", Name: "Security", Icon: "shield-halved", Description: "Security scanning and compliance"},
	"nosql":          {Key: "nosql", Name: "NoSQL", Icon: "layer-group", Description: "NoSQL database operations"},
	"sql":            {Key: "sql", Name: "SQL", Icon: "database", Description: "Relational database queries"},
	"script":         {Key: "script", Name: "Script", Icon: "terminal", Description: "Execute scripts and commands"},
	"trigger":        {Key: "trigger", Name: "Triggers", Icon: "bolt-lightning", Description: "Start a Flow"},
	"error":          {Key: "error", Name: "Error Handling", Icon: "triangle-exclamation", Description: "Handle and recover from flow errors"},
	"agent":          {Key: "agent", Name: "Agent", Icon: "robot", Description: "Interact with Flomation Agents"},
	"messaging":      {Key: "messaging", Name: "Messaging", Icon: "comments", Description: "Send messages via various channels"},
	"notion":         {Key: "notion", Name: "Notion", Icon: "notion", Description: "Search, create, and manage pages, databases, and content in Notion"},
	"slack":          {Key: "slack", Name: "Slack", Icon: "slack", Description: "Slack workspace messaging, channels, and integrations"},
	"web":            {Key: "web", Name: "Web", Icon: "globe", Description: "Web browsing, search, and HTTP request operations"},
	"linear":         {Key: "linear", Name: "Linear", Icon: "linear", Description: "Manage issues, projects, and teams in Linear"},
	"stripe":         {Key: "stripe", Name: "Stripe", Icon: "stripe", Description: "Accept payments and manage customers, subscriptions and invoices in Stripe"},
	"elevenlabs":     {Key: "elevenlabs", Name: "ElevenLabs", Icon: "microphone", Description: "AI voice synthesis and speech recognition"},
	"subflow":        {Key: "subflow", Name: "Sub-Flow", Icon: "layer-group", Description: "Reusable sub-flow subroutines"},
	"string":         {Key: "string", Name: "String", Icon: "font", Description: "String manipulation and text operations"},
	"social":         {Key: "social", Name: "Social Media", Icon: "comments", Description: "Publish and manage content on social media platforms"},
	"google":         {Key: "google", Name: "Google", Icon: "google", Description: "Google Workspace integrations"},
	"mailchimp":      {Key: "mailchimp", Name: "Mailchimp", Icon: "mailchimp", Description: "Manage audiences, members, tags, and campaigns in Mailchimp"},
	"makefile":       {Key: "makefile", Name: "Makefile", Icon: "gears", Description: "Parse and execute Makefile targets"},
	"twilio":         {Key: "twilio", Name: "Twilio", Icon: "phone", Description: "Twilio voice call and SMS actions"},
	"microsoft":      {Key: "microsoft", Name: "Microsoft", Icon: "microsoft", Description: "Microsoft 365 integrations"},
	"webflow":        {Key: "webflow", Name: "Webflow", Icon: "webflow", Description: "Manage Webflow sites, CMS collections, pages, and forms"},
	"journey":        {Key: "journey", Name: "Journey", Icon: "route", Description: "Route planning, journey optimisation, and printable itineraries"},
	"plan":           {Key: "plan", Name: "Plan", Icon: "list-check", Description: "Create and manage autonomous multi-step plans the agent progresses on its own"},
	"opentofu":       {Key: "infrastructure", Name: "Infrastructure", Icon: "server", Description: "Provision and manage infrastructure as code", SubKey: "opentofu", SubName: "OpenTofu", SubIcon: "cubes", SubDescription: "Infrastructure as Code — run OpenTofu plan, apply, and destroy"},
	"databricks":     {Key: "data-warehouse", Name: "Data Warehouse", Icon: "cubes-stacked", Description: "Query and orchestrate data warehouses and lakehouses", SubKey: "databricks", SubName: "Databricks", SubIcon: "database", SubDescription: "Run SQL, jobs, and models against a Databricks lakehouse"},
	"hubspot":        {Key: "crm", Name: "CRM", Icon: "people-group", Description: "Customer relationship management — contacts, companies, deals, and tickets", SubKey: "hubspot", SubName: "HubSpot", SubIcon: "hubspot", SubDescription: "Manage contacts, companies, deals, and tickets in the HubSpot CRM"},
	// E-Commerce uses 3-segment action IDs (ecommerce/shopify/order_create),
	// so the sub-group (Shopify) is resolved from subCategoryMetadata below —
	// no inline Sub* fields here (getCategoryForAction would overwrite them).
	"ecommerce": {Key: "ecommerce", Name: "E-Commerce", Icon: "cart-shopping", Description: "Online store platforms — orders, products, and customers"},
	// Scheduling uses 3-segment action IDs (scheduling/calendly/event_get), so
	// the sub-group (Calendly) is resolved from subCategoryMetadata below.
	"scheduling": {Key: "scheduling", Name: "Scheduling", Icon: "calendar", Description: "Meeting scheduling and booking platforms"},
	// Helpdesk uses 3-segment action IDs (helpdesk/zendesk/ticket_create), so
	// the sub-group (Zendesk) is resolved from subCategoryMetadata below.
	"helpdesk": {Key: "helpdesk", Name: "Helpdesk", Icon: "headset", Description: "Customer support and ticketing platforms"},
	// DevOps uses 3-segment action IDs (devops/jenkins/job_trigger), so the
	// sub-group (Jenkins) is resolved from subCategoryMetadata below.
	"devops": {Key: "devops", Name: "DevOps", Icon: "gears", Description: "Automate your build, test, and deploy workflows — trigger jobs, watch builds, and manage your CI/CD servers"},
	// UK Government uses 3-segment action IDs (ukgov/companieshouse/get_company),
	// so the sub-group (the agency) is resolved from subCategoryMetadata below.
	"ukgov": {Key: "ukgov", Name: "UK Government", Icon: "landmark", Description: "UK government agency data — Companies House, DVLA, Police, Food Standards and more"},
}

// subCategoryMetadata maps sub-paths (e.g. "aws/s3") to display metadata.
var subCategoryMetadata = map[string]struct {
	Name        string
	Icon        string
	Description string
}{
	"aws/s3":                  {Name: "S3", Icon: "box-archive", Description: "Simple Storage Service operations"},
	"aws/ec2":                 {Name: "EC2", Icon: "server", Description: "Elastic Compute Cloud operations"},
	"social/linkedin":         {Name: "LinkedIn", Icon: "linkedin", Description: "Publish posts, manage content, and read analytics on LinkedIn"},
	"social/facebook":         {Name: "Facebook", Icon: "facebook", Description: "Publish posts, manage pages, and read insights on Facebook"},
	"google/drive":            {Name: "Drive", Icon: "folder", Description: "Google Drive file storage and management"},
	"google/sheets":           {Name: "Sheets", Icon: "table", Description: "Google Sheets spreadsheet operations"},
	"google/docs":             {Name: "Docs", Icon: "file-lines", Description: "Google Docs document operations"},
	"google/slides":           {Name: "Slides", Icon: "display", Description: "Google Slides presentation operations"},
	"microsoft/outlook":       {Name: "Outlook", Icon: "envelope", Description: "Microsoft Outlook email operations"},
	"microsoft/teams":         {Name: "Teams", Icon: "user-group", Description: "Microsoft Teams messaging and channel operations"},
	"microsoft/calendar":      {Name: "Calendar", Icon: "calendar", Description: "Microsoft Outlook calendar event management"},
	"microsoft/excel":         {Name: "Excel", Icon: "table", Description: "Microsoft Excel Online spreadsheet operations"},
	"microsoft/onedrive":      {Name: "OneDrive", Icon: "folder", Description: "Microsoft OneDrive file storage and management"},
	"microsoft/sharepoint":    {Name: "SharePoint", Icon: "globe", Description: "Microsoft SharePoint sites, lists, and document libraries"},
	"microsoft/word":          {Name: "Word", Icon: "file-lines", Description: "Microsoft Word Online document operations"},
	"microsoft/powerpoint":    {Name: "PowerPoint", Icon: "display", Description: "Microsoft PowerPoint Online presentation operations"},
	"google/gmail":            {Name: "Gmail", Icon: "gmail", Description: "Google Gmail email operations"},
	"google/calendar":         {Name: "Calendar", Icon: "calendar", Description: "Google Calendar event management"},
	"messaging/telegram":      {Name: "Telegram", Icon: "telegram", Description: "Telegram Bot messaging operations"},
	"messaging/discord":       {Name: "Discord", Icon: "discord", Description: "Discord messaging and webhook operations"},
	"ecommerce/shopify":       {Name: "Shopify", Icon: "shopify", Description: "Manage orders and products in your Shopify store"},
	"scheduling/calendly":     {Name: "Calendly", Icon: "calendly", Description: "Manage Calendly event types, scheduled events, invitees, and scheduling links"},
	"scheduling/calcom":       {Name: "Cal.com", Icon: "calcom", Description: "Manage Cal.com bookings, event types, schedules, availability slots, teams, and webhooks"},
	"scheduling/acuity":       {Name: "Acuity", Icon: "acuity", Description: "Manage Acuity Scheduling appointments, availability, clients, appointment types and calendars"},
	"helpdesk/zendesk":        {Name: "Zendesk", Icon: "zendesk", Description: "Manage tickets, users, and organizations in Zendesk Support"},
	"devops/jenkins":          {Name: "Jenkins", Icon: "jenkins", Description: "Trigger and manage Jenkins jobs and builds, and control the Jenkins server"},
	"ukgov/companieshouse":    {Name: "Companies House", Icon: "building", Description: "UK company registry — search companies, officers, filings, PSCs and charges"},
	"ukgov/dvla":              {Name: "DVLA", Icon: "car", Description: "UK vehicle data — tax, MOT status and vehicle details"},
	"ukgov/foodstandards":     {Name: "Food Standards Agency", Icon: "utensils", Description: "UK food hygiene ratings (FHRS)"},
	"ukgov/police":            {Name: "Police UK", Icon: "shield-halved", Description: "UK street-level crime, stop-and-search and police force data"},
	"ukgov/environmentagency": {Name: "Environment Agency", Icon: "water", Description: "UK flood warnings, flood areas and river/rainfall monitoring"},
	"ukgov/postcodes":         {Name: "Postcodes", Icon: "location-dot", Description: "UK postcode lookup, validation and geocoding"},
}

func getCategoryForAction(actionID string) *api.ActionCategory {
	parts := strings.Split(actionID, "/")
	if len(parts) == 0 {
		return nil
	}
	cat, ok := categoryMetadata[parts[0]]
	if !ok {
		return nil
	}

	// For 3+ segment action IDs, populate sub-category fields
	if len(parts) >= 3 {
		subPath := parts[0] + "/" + parts[1]
		if sub, ok := subCategoryMetadata[subPath]; ok {
			cat.SubKey = subPath
			cat.SubName = sub.Name
			cat.SubIcon = sub.Icon
			cat.SubDescription = sub.Description
		} else {
			// Auto-generate from directory name
			cat.SubKey = subPath
			cat.SubName = strings.ToUpper(parts[1][:1]) + parts[1][1:]
		}
	}

	return &cat
}

// dynamicOptionsMetadata maps "actionID#inputName" to a dynamic-options
// source injected at serve time — the same in-code-override pattern as
// categoryMetadata above. The input's static Options from the manifest
// remain the editor's fallback when the fetch fails, so entries here must
// point at endpoints returning the same {"options": [{name, value}]} shape.
var dynamicOptionsMetadata = map[string]api.InputDynamicOptions{
	"ai/openrouter#model": {Endpoint: "/api/v1/action/options/openrouter-models"},
	// Zendesk live dropdowns: the Groups and Organizations pickers each resolve
	// from a proxy endpoint, forwarding the node's subdomain/email/api_token so
	// the api can call the account's API server-side (api_token is a secret,
	// resolved from the environment — the plaintext never transits the browser).
	"helpdesk/zendesk/ticket_create#group_id": {
		Endpoint: "/api/v1/action/options/zendesk-groups",
		Params:   []string{"subdomain", "email", "api_token"},
	},
	"helpdesk/zendesk/ticket_update#group_id": {
		Endpoint: "/api/v1/action/options/zendesk-groups",
		Params:   []string{"subdomain", "email", "api_token"},
	},
	"helpdesk/zendesk/ticket_get_all#group_id": {
		Endpoint: "/api/v1/action/options/zendesk-groups",
		Params:   []string{"subdomain", "email", "api_token"},
	},
	"helpdesk/zendesk/user_create#organization_id": {
		Endpoint: "/api/v1/action/options/zendesk-organizations",
		Params:   []string{"subdomain", "email", "api_token"},
	},
	"helpdesk/zendesk/user_update#organization_id": {
		Endpoint: "/api/v1/action/options/zendesk-organizations",
		Params:   []string{"subdomain", "email", "api_token"},
	},
	"ai/ollama#model": {
		Endpoint: "/api/v1/action/options/ollama-models",
		Params:   []string{"endpoint", "api_key"},
	},
	// Jenkins "Job" pickers. Every action that targets a job resolves its
	// dropdown from the same proxy, which forwards the node's base_url /
	// username / api_token and lists the instance's jobs server-side (api_token
	// is a secret resolved from the environment — the plaintext never transits
	// the browser). The static (empty) options remain the fallback for manual
	// entry when the fetch fails.
	"devops/jenkins/job_trigger#job":        jenkinsJobsOption,
	"devops/jenkins/job_trigger_params#job": jenkinsJobsOption,
	"devops/jenkins/job_copy#job":           jenkinsJobsOption,
	"devops/jenkins/job_get#job":            jenkinsJobsOption,
	"devops/jenkins/job_enable#job":         jenkinsJobsOption,
	"devops/jenkins/job_disable#job":        jenkinsJobsOption,
	"devops/jenkins/job_delete#job":         jenkinsJobsOption,
	"devops/jenkins/build_get_all#job":      jenkinsJobsOption,
	"devops/jenkins/build_get#job":          jenkinsJobsOption,
	"devops/jenkins/build_console#job":      jenkinsJobsOption,
	"devops/jenkins/build_stop#job":         jenkinsJobsOption,
}

// jenkinsJobsOption is the shared dynamic-options marker for every Jenkins
// "Job" input — declared once so the 11 entries above stay identical.
var jenkinsJobsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/jenkins-jobs",
	Params:   []string{"base_url", "username", "api_token"},
}

func (s *Service) getActions(c *gin.Context) {
	actions, err := s.persistence.GetActions()
	if err != nil {
		log.WithFields(log.Fields{
			"error": err,
		}).Error("unable to get actions")
		c.AbortWithStatus(http.StatusBadRequest)
		return
	}

	mappedActions := make(map[string]api.Action)

	for _, a := range actions {
		a.Type, _ = strconv.ParseInt(a.ActionType, 10, 64)

		if a.Inputs != nil {
			var inputs []api.InputDefinition
			if err := json.Unmarshal(a.Inputs.([]byte), &inputs); err != nil {
				log.WithFields(log.Fields{
					"error": err,
				}).Error("unable to get actions")
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}
			for idx := range inputs {
				if dyn, ok := dynamicOptionsMetadata[a.ID+"#"+inputs[idx].Name]; ok {
					d := dyn
					inputs[idx].DynamicOptions = &d
				}
			}
			a.Inputs = inputs
		}

		if a.Outputs != nil {
			var outputs []api.OutputDefinition
			if err := json.Unmarshal(a.Outputs.([]byte), &outputs); err != nil {
				log.WithFields(log.Fields{
					"error": err,
				}).Error("unable to get actions")
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}
			a.Outputs = outputs
		}

		a.Category = getCategoryForAction(a.ID)
		mappedActions[a.ID] = *a
	}

	c.JSON(http.StatusOK, mappedActions)
}
