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
	"image":          {Key: "image", Name: "Image", Icon: "image", Description: "Process images with ImageMagick: resize, convert, crop and info"},
	"video":          {Key: "video", Name: "Video", Icon: "film", Description: "Process audio and video with ffmpeg: extract audio, thumbnails, trimming and info"},
	"graphics":       {Key: "graphics", Name: "Graphics", Icon: "pen", Description: "Generate animated graphics: titles, lower-thirds and counters as transparent overlays"},
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
	"jira":           {Key: "project-management", Name: "Project Management", Icon: "list-check", Description: "Plan and track work — issues, tasks, boards, and projects", SubKey: "jira", SubName: "Jira", SubIcon: "jira", SubDescription: "Create and manage issues, comments, attachments, worklogs, and users in Jira"},
	"trello":         {Key: "project-management", Name: "Project Management", Icon: "list-check", Description: "Plan and track work — issues, tasks, boards, and projects", SubKey: "trello", SubName: "Trello", SubIcon: "trello", SubDescription: "Create and manage boards, lists, cards, checklists, labels, and members in Trello"},
	"asana":          {Key: "project-management", Name: "Project Management", Icon: "list-check", Description: "Plan and track work — issues, tasks, boards, and projects", SubKey: "asana", SubName: "Asana", SubIcon: "asana", SubDescription: "Create and manage tasks, subtasks, projects, sections, tags, and users in Asana"},
	"monday":         {Key: "project-management", Name: "Project Management", Icon: "list-check", Description: "Plan and track work — issues, tasks, boards, and projects", SubKey: "monday", SubName: "Monday.com", SubIcon: "monday", SubDescription: "Create and manage boards, groups, columns, items, and updates in Monday.com"},
	"stripe":         {Key: "stripe", Name: "Stripe", Icon: "stripe", Description: "Accept payments and manage customers, subscriptions and invoices in Stripe"},
	"quickbooks":     {Key: "quickbooks", Name: "QuickBooks Online", Icon: "quickbooks", Description: "Manage customers, invoices, bills, payments and the ledger in QuickBooks Online"},
	"xero":           {Key: "xero", Name: "Xero", Icon: "xero", Description: "Manage contacts, invoices, bills, payments and the ledger in Xero"},
	"elevenlabs":     {Key: "elevenlabs", Name: "ElevenLabs", Icon: "microphone", Description: "AI voice synthesis and speech recognition"},
	"subflow":        {Key: "subflow", Name: "Sub-Flow", Icon: "layer-group", Description: "Reusable sub-flow subroutines"},
	"string":         {Key: "string", Name: "String", Icon: "font", Description: "String manipulation and text operations"},
	"social":         {Key: "social", Name: "Social Media", Icon: "comments", Description: "Publish and manage content on social media platforms"},
	"google":         {Key: "google", Name: "Google", Icon: "google", Description: "Google Workspace integrations"},
	"mailchimp":      {Key: "marketing", Name: "Marketing", Icon: "bullhorn", Description: "Email and marketing platforms — contacts, campaigns, and transactional email", SubKey: "mailchimp", SubName: "Mailchimp", SubIcon: "mailchimp", SubDescription: "Manage audiences, members, tags, and campaigns in Mailchimp"},
	"makefile":       {Key: "makefile", Name: "Makefile", Icon: "gears", Description: "Parse and execute Makefile targets"},
	"twilio":         {Key: "twilio", Name: "Twilio", Icon: "phone", Description: "Twilio voice call and SMS actions"},
	"microsoft":      {Key: "microsoft", Name: "Microsoft", Icon: "microsoft", Description: "Microsoft 365 integrations"},
	"webflow":        {Key: "webflow", Name: "Webflow", Icon: "webflow", Description: "Manage Webflow sites, CMS collections, pages, and forms"},
	"journey":        {Key: "journey", Name: "Journey", Icon: "route", Description: "Route planning, journey optimisation, and printable itineraries"},
	"plan":           {Key: "plan", Name: "Plan", Icon: "list-check", Description: "Create and manage autonomous multi-step plans the agent progresses on its own"},
	// OpenTofu predates the infrastructure/ directory and still uses two-segment
	// action IDs (opentofu/apply), so its sub-group is carried inline here rather
	// than resolved from subCategoryMetadata: getCategoryForAction only populates
	// Sub* for IDs of three segments or more.
	//
	// Its Key/Name/Icon/Description MUST stay byte-identical to the
	// "infrastructure" entry below. Both emit Key "infrastructure" and so feed the
	// same group header; any drift and the header changes depending on which
	// action the editor happened to read first. Same trap as Mailchimp/Marketing.
	"opentofu":   {Key: "infrastructure", Name: "Infrastructure", Icon: "server", Description: "Provision and operate your infrastructure — Kubernetes clusters, Helm releases, and infrastructure as code", SubKey: "opentofu", SubName: "OpenTofu", SubIcon: "cubes", SubDescription: "Infrastructure as Code — run OpenTofu plan, apply, and destroy"},
	"databricks": {Key: "data-warehouse", Name: "Data Warehouse", Icon: "cubes-stacked", Description: "Query and orchestrate data warehouses and lakehouses", SubKey: "databricks", SubName: "Databricks", SubIcon: "database", SubDescription: "Run SQL, jobs, and models against a Databricks lakehouse"},
	"hubspot":    {Key: "crm", Name: "CRM", Icon: "people-group", Description: "Customer relationship management — contacts, companies, deals, and tickets", SubKey: "hubspot", SubName: "HubSpot", SubIcon: "hubspot", SubDescription: "Manage contacts, companies, deals, and tickets in the HubSpot CRM"},
	// CRM uses 3-segment action IDs (crm/salesforce/lead_create), so the
	// sub-group (Salesforce) is resolved from subCategoryMetadata below and no
	// Sub* fields belong here — getCategoryForAction would overwrite them.
	//
	// HubSpot's 2-segment remap entry above duplicates this entry's
	// Key/Name/Icon/Description verbatim; keep them byte-identical or the CRM
	// group header changes depending on which action the editor happened to
	// read first. Same trap as Mailchimp/Marketing and OpenTofu/Infrastructure.
	//
	// HubSpot deliberately stays at actions/hubspot/ rather than moving under
	// actions/crm/: action IDs are derived from the executor directory path, so
	// relocating it would rename all 28 of its actions, and the api DELETES
	// actions rows absent from a freshly-ingested manifest — every saved flow
	// with a HubSpot node would fail to resolve. The remap gets the shared
	// palette header at none of that cost.
	"crm": {Key: "crm", Name: "CRM", Icon: "people-group", Description: "Customer relationship management — contacts, companies, deals, and tickets"},
	// E-Commerce uses 3-segment action IDs (ecommerce/shopify/order_create),
	// so the sub-group (Shopify) is resolved from subCategoryMetadata below —
	// no inline Sub* fields here (getCategoryForAction would overwrite them).
	"ecommerce": {Key: "ecommerce", Name: "E-Commerce", Icon: "cart-shopping", Description: "Online store platforms — orders, products, and customers"},
	// CMS uses 3-segment action IDs (cms/wordpress/post_create), so the
	// sub-group (WordPress) is resolved from subCategoryMetadata below.
	"cms": {Key: "cms", Name: "CMS", Icon: "newspaper", Description: "Content management systems — publish and manage posts, pages, and media"},
	// Scheduling uses 3-segment action IDs (scheduling/calendly/event_get), so
	// the sub-group (Calendly) is resolved from subCategoryMetadata below.
	"scheduling": {Key: "scheduling", Name: "Scheduling", Icon: "calendar", Description: "Meeting scheduling and booking platforms"},
	// Helpdesk uses 3-segment action IDs (helpdesk/zendesk/ticket_create), so
	// the sub-group (Zendesk) is resolved from subCategoryMetadata below.
	"helpdesk": {Key: "helpdesk", Name: "Helpdesk", Icon: "headset", Description: "Customer support and ticketing platforms"},
	// Message Brokers uses 3-segment action IDs (messagebrokers/mqtt/publish), so
	// the sub-group (MQTT) is resolved from subCategoryMetadata below. The
	// executor directory is "messagebrokers" (one word) because it has to be a
	// valid Go package name; the display name is set here.
	"messagebrokers": {Key: "messagebrokers", Name: "Message Brokers", Icon: "arrow-right-arrow-left", Description: "Publish and subscribe to message brokers — move events between systems in real time"},
	// DevOps uses 3-segment action IDs (devops/jenkins/job_trigger), so the
	// sub-group (Jenkins) is resolved from subCategoryMetadata below.
	"devops": {Key: "devops", Name: "DevOps", Icon: "gears", Description: "Automate your build, test, and deploy workflows — trigger jobs, watch builds, and manage your CI/CD servers"},
	// UK Government uses 3-segment action IDs (ukgov/companieshouse/get_company),
	// so the sub-group (the agency) is resolved from subCategoryMetadata below.
	"ukgov": {Key: "ukgov", Name: "UK Government", Icon: "landmark", Description: "UK government agency data — Companies House, DVLA, Police, Food Standards and more"},
	// Marketing uses 3-segment action IDs (marketing/sendgrid/mail_send), so the
	// sub-group (SendGrid) is resolved from subCategoryMetadata below. Mailchimp's
	// 2-segment remap entry above duplicates this entry's Key/Name/Icon/Description
	// verbatim — keep them byte-identical or the group header drifts.
	"marketing": {Key: "marketing", Name: "Marketing", Icon: "bullhorn", Description: "Email and marketing platforms — contacts, campaigns, and transactional email"},
	// Infrastructure uses 3-segment action IDs (infrastructure/kubernetes/pod_list,
	// infrastructure/helm/release_install), so the sub-group is resolved from
	// subCategoryMetadata below and no Sub* fields belong here — getCategoryForAction
	// would overwrite them anyway. The "opentofu" 2-segment remap above duplicates
	// this entry's Key/Name/Icon/Description verbatim; keep them byte-identical.
	"infrastructure": {Key: "infrastructure", Name: "Infrastructure", Icon: "server", Description: "Provision and operate your infrastructure — Kubernetes clusters, Helm releases, and infrastructure as code"},
	// Forms uses 3-segment action IDs (forms/typeform/form_create), so the
	// sub-group (the provider) is resolved from subCategoryMetadata below.
	"forms": {Key: "forms", Name: "Forms", Icon: "clipboard-list", Description: "Create forms, collect responses and trigger flows from external form providers"},
	// Vector Database uses 3-segment action IDs
	// (vectordatabase/pgvector/document_search), so the sub-group (pgvector) is
	// resolved from subCategoryMetadata below — no inline Sub* fields here, or
	// getCategoryForAction would overwrite them. The executor directory is
	// "vectordatabase" (one word) because it has to be a valid Go package name;
	// the display name is set here.
	"vectordatabase": {Key: "vectordatabase", Name: "Vector Database", Icon: "circle-nodes", Description: "Store and search embeddings — semantic search, similarity lookups, and retrieval-augmented generation"},
	// Azure uses 3-segment action IDs (azure/storage/blob_upload), so the
	// sub-group (Storage / Cosmos DB / Entra ID) is resolved from
	// subCategoryMetadata below. The Azure OpenAI chat node (ai/azure_openai)
	// and Azure AI Search (vectordatabase/azureaisearch) live under their
	// capability categories, not here.
	"azure":  {Key: "azure", Name: "Azure", Icon: "azure", Description: "Microsoft Azure integrations"},
	"oracle": {Key: "oracle", Name: "Oracle Cloud", Icon: "oracle", Description: "Oracle Cloud Infrastructure (OCI) integrations"},
}

// subCategoryMetadata maps sub-paths (e.g. "aws/s3") to display metadata.
var subCategoryMetadata = map[string]struct {
	Name        string
	Icon        string
	Description string
}{
	"aws/s3":               {Name: "S3", Icon: "box-archive", Description: "Simple Storage Service operations"},
	"aws/ec2":              {Name: "EC2", Icon: "server", Description: "Elastic Compute Cloud operations"},
	"aws/rds":              {Name: "RDS", Icon: "database", Description: "Relational Database Service operations"},
	"aws/vpc":              {Name: "VPC", Icon: "circle-nodes", Description: "Virtual Private Cloud networking — subnets, route tables, gateways, peering and VPN"},
	"aws/elbv2":            {Name: "Elastic Load Balancing", Icon: "arrows-split-up-and-left", Description: "Application, Network and Gateway load balancers — target groups, listeners, rules and target health"},
	"aws/autoscaling":      {Name: "Auto Scaling", Icon: "arrows-up-down", Description: "EC2 Auto Scaling groups — desired capacity, scaling policies, scheduled actions and instance refresh"},
	"aws/route53":          {Name: "Route 53", Icon: "globe", Description: "DNS and traffic management — hosted zones, records, health checks and query logging"},
	"aws/route53domains":   {Name: "Route 53 Domains", Icon: "id-badge", Description: "Domain registration — register, transfer, renew and manage domains, contacts and nameservers"},
	"aws/cloudwatch":       {Name: "CloudWatch", Icon: "chart-line", Description: "Metrics, alarms and dashboards — publish metric data, manage alarms and build dashboards"},
	"aws/cloudwatchlogs":   {Name: "CloudWatch Logs", Icon: "file-lines", Description: "Log groups, streams and events — write and query logs, and manage metric and subscription filters"},
	"aws/eventbridge":      {Name: "EventBridge", Icon: "bolt", Description: "Event rules and targets — route events with rules, manage targets and publish custom events"},
	"aws/iam":              {Name: "IAM", Icon: "shield-halved", Description: "Identity and Access Management — users, groups, roles, policies, access keys and instance profiles"},
	"aws/kms":              {Name: "KMS", Icon: "key", Description: "Key Management Service — keys, aliases, encryption, data keys, signing and grants"},
	"aws/secretsmanager":   {Name: "Secrets Manager", Icon: "lock", Description: "Store, retrieve and rotate secrets — secret values, versions, rotation and resource policies"},
	"social/linkedin":      {Name: "LinkedIn", Icon: "linkedin", Description: "Publish posts, manage content, and read analytics on LinkedIn"},
	"social/facebook":      {Name: "Facebook", Icon: "facebook", Description: "Publish posts, manage pages, and read insights on Facebook"},
	"google/drive":         {Name: "Drive", Icon: "folder", Description: "Google Drive file storage and management"},
	"google/sheets":        {Name: "Sheets", Icon: "table", Description: "Google Sheets spreadsheet operations"},
	"google/docs":          {Name: "Docs", Icon: "file-lines", Description: "Google Docs document operations"},
	"google/slides":        {Name: "Slides", Icon: "display", Description: "Google Slides presentation operations"},
	"microsoft/outlook":    {Name: "Outlook", Icon: "envelope", Description: "Microsoft Outlook email operations"},
	"microsoft/teams":      {Name: "Teams", Icon: "user-group", Description: "Microsoft Teams messaging and channel operations"},
	"microsoft/calendar":   {Name: "Calendar", Icon: "calendar", Description: "Microsoft Outlook calendar event management"},
	"microsoft/excel":      {Name: "Excel", Icon: "table", Description: "Microsoft Excel Online spreadsheet operations"},
	"microsoft/onedrive":   {Name: "OneDrive", Icon: "folder", Description: "Microsoft OneDrive file storage and management"},
	"microsoft/sharepoint": {Name: "SharePoint", Icon: "globe", Description: "Microsoft SharePoint sites, lists, and document libraries"},
	"microsoft/word":       {Name: "Word", Icon: "file-lines", Description: "Microsoft Word Online document operations"},
	"microsoft/powerpoint": {Name: "PowerPoint", Icon: "display", Description: "Microsoft PowerPoint Online presentation operations"},
	"google/gmail":         {Name: "Gmail", Icon: "gmail", Description: "Google Gmail email operations"},
	"google/calendar":      {Name: "Calendar", Icon: "calendar", Description: "Google Calendar event management"},
	"messaging/telegram":   {Name: "Telegram", Icon: "telegram", Description: "Telegram Bot messaging operations"},
	"messaging/discord":    {Name: "Discord", Icon: "discord", Description: "Discord messaging and webhook operations"},
	"ecommerce/shopify":    {Name: "Shopify", Icon: "shopify", Description: "Manage orders and products in your Shopify store"},
	// Description must stay byte-identical to CategoryDescription in
	// executor/actions/crm/salesforce/category.go.
	"crm/salesforce":                 {Name: "Salesforce", Icon: "salesforce", Description: "Manage Salesforce leads, contacts, accounts, opportunities, cases, tasks, and any custom object"},
	"crm/apollo":                     {Name: "Apollo", Icon: "apollo", Description: "Enrich, search and manage Apollo.io contacts, accounts, deals and sequences"},
	"ecommerce/woocommerce":          {Name: "WooCommerce", Icon: "woocommerce", Description: "Manage customers, orders, products, and coupons in your WooCommerce store"},
	"cms/wordpress":                  {Name: "WordPress", Icon: "wordpress", Description: "Manage posts, pages, users, comments, categories, and tags on your WordPress site"},
	"scheduling/calendly":            {Name: "Calendly", Icon: "calendly", Description: "Manage Calendly event types, scheduled events, invitees, and scheduling links"},
	"scheduling/calcom":              {Name: "Cal.com", Icon: "calcom", Description: "Manage Cal.com bookings, event types, schedules, availability slots, teams, and webhooks"},
	"scheduling/acuity":              {Name: "Acuity", Icon: "acuity", Description: "Manage Acuity Scheduling appointments, availability, clients, appointment types and calendars"},
	"helpdesk/zendesk":               {Name: "Zendesk", Icon: "zendesk", Description: "Manage tickets, users, and organizations in Zendesk Support"},
	"helpdesk/intercom":              {Name: "Intercom", Icon: "intercom", Description: "Manage contacts, companies, conversations, tickets, tags, notes, and articles in Intercom"},
	"devops/jenkins":                 {Name: "Jenkins", Icon: "jenkins", Description: "Trigger and manage Jenkins jobs and builds, and control the Jenkins server"},
	"devops/azuredevops":             {Name: "Azure DevOps", Icon: "azure", Description: "Azure DevOps — work items, repositories, pull requests, pipelines and builds"},
	"forms/typeform":                 {Name: "Typeform", Icon: "clipboard-list", Description: "Create Typeform forms, read responses and manage webhooks"},
	"forms/jotform":                  {Name: "JotForm", Icon: "clipboard-list", Description: "Create JotForm forms, read submissions and manage webhooks"},
	"forms/surveymonkey":             {Name: "SurveyMonkey", Icon: "clipboard-list", Description: "Create SurveyMonkey surveys, read responses, manage collectors and webhooks"},
	"forms/googleforms":              {Name: "Google Forms", Icon: "clipboard-list", Description: "Create Google Forms, add questions and read responses (uses your Google connection)"},
	"messagebrokers/mqtt":            {Name: "MQTT", Icon: "tower-broadcast", Description: "Publish messages to an MQTT broker, read retained values, and wait for messages on a topic"},
	"messagebrokers/azureservicebus": {Name: "Azure Service Bus", Icon: "azure", Description: "Azure Service Bus — send and receive messages, work through queues and topics, and schedule messages for later"},
	"ukgov/companieshouse":           {Name: "Companies House", Icon: "briefcase", Description: "UK company registry — search companies, officers, filings, PSCs and charges"},
	"ukgov/dvla":                     {Name: "DVLA", Icon: "truck-ramp-box", Description: "UK vehicle data — tax, MOT status and vehicle details"},
	"ukgov/foodstandards":            {Name: "Food Standards Agency", Icon: "star", Description: "UK food hygiene ratings (FHRS)"},
	"ukgov/police":                   {Name: "Police UK", Icon: "shield-halved", Description: "UK street-level crime, stop-and-search and police force data"},
	"ukgov/environmentagency":        {Name: "Environment Agency", Icon: "leaf", Description: "UK flood warnings, flood areas and river/rainfall monitoring"},
	"ukgov/postcodes":                {Name: "Postcodes", Icon: "map", Description: "UK postcode lookup, validation and geocoding"},
	"ukgov/parliament":               {Name: "UK Parliament", Icon: "landmark", Description: "UK Parliament — members, bills, Commons votes and written questions"},
	"ukgov/ons":                      {Name: "ONS", Icon: "chart-line", Description: "UK economic statistics from the Office for National Statistics"},
	"ukgov/dvsa":                     {Name: "DVSA", Icon: "wrench", Description: "UK MOT test history"},
	"ukgov/charitycommission":        {Name: "Charity Commission", Icon: "hand", Description: "The register of charities for England & Wales"},
	"ukgov/bankholidays":             {Name: "Bank Holidays", Icon: "calendar", Description: "UK bank holiday dates by region"},
	"ukgov/landregistry":             {Name: "Land Registry", Icon: "house", Description: "UK property sold-price data (Price Paid)"},
	"marketing/sendgrid":             {Name: "SendGrid", Icon: "sendgrid", Description: "Send transactional email and manage contacts, lists, templates, and suppressions in SendGrid"},
	"infrastructure/kubernetes":      {Name: "Kubernetes", Icon: "kubernetes", Description: "Operate a Kubernetes cluster — restart and scale deployments, read pod logs, run jobs, manage config, and drain nodes"},
	"infrastructure/helm":            {Name: "Helm", Icon: "helm", Description: "Install, upgrade, roll back and inspect Helm releases on a Kubernetes cluster"},
	// Mirrors executor/actions/infrastructure/awx/category.go's consts — this map,
	// not the Go const, is what the editor reads at serve time, so the two must
	// stay in step. The top-level "infrastructure" entry above is deliberately
	// untouched: it and the "opentofu" remap must stay byte-identical to each
	// other, and the AAP/AWX story is told here, in the sub-group, which is where
	// the operator actually reads it.
	"infrastructure/awx":      {Name: "AAP / AWX", Icon: "ansible", Description: "Ansible Automation Platform / AWX — launch existing job templates and workflows, watch jobs to completion, and manage inventories, hosts, projects, credentials and schedules. This node talks to the AWX/AAP controller's API; it does not run playbooks itself."},
	"vectordatabase/pgvector": {Name: "pgvector", Icon: "database", Description: "Store and query embeddings in a PostgreSQL database with the pgvector extension"},
	// The Azure sub-groups mirror the executor category.go consts of
	// executor/actions/azure/{storage,cosmosdb,entra} and
	// executor/actions/vectordatabase/azureaisearch — this map, not the Go
	// const, is what the editor reads at serve time, so the descriptions must
	// stay byte-identical to those files.
	"azure/storage":                {Name: "Storage", Icon: "box-archive", Description: "Azure Blob Storage — containers, blobs, tiers, tags, and shared access links"},
	"azure/cosmosdb":               {Name: "Cosmos DB", Icon: "database", Description: "Azure Cosmos DB (NoSQL) — databases, containers, items, queries, and throughput"},
	"azure/entra":                  {Name: "Entra ID", Icon: "id-badge", Description: "Microsoft Entra ID (Azure AD) — users, groups, membership, licences, and guest invites"},
	"azure/tables":                 {Name: "Table Storage", Icon: "table", Description: "Azure Table Storage — tables and entities, with queries and partial updates"},
	"azure/files":                  {Name: "Files", Icon: "folder-tree", Description: "Azure Files — file shares, directories and files, with time-limited share links"},
	"azure/compute":                {Name: "Virtual Machines", Icon: "server", Description: "Azure Virtual Machines — lifecycle, network security groups, disks, snapshots, images, SSH keys and tags"},
	"oracle/compute":               {Name: "Compute", Icon: "server", Description: "Oracle Cloud Compute — instance lifecycle, shapes, images, VNICs, networking and tags"},
	"oracle/objectstorage":         {Name: "Object Storage", Icon: "box", Description: "Oracle Cloud Object Storage — buckets, objects, copy/rename, and pre-authenticated (presigned) request URLs"},
	"oracle/autonomousdatabase":    {Name: "Autonomous Database", Icon: "database", Description: "Oracle Cloud Autonomous Database — provision, scale, back up, clone and generate connection wallets for self-driving Oracle databases"},
	"oracle/networking":            {Name: "Networking", Icon: "network-wired", Description: "Oracle Cloud Networking — VCNs, subnets, security lists, route tables, gateways, network security groups, DHCP options and public IPs"},
	"oracle/blockvolume":           {Name: "Block Volumes", Icon: "hard-drive", Description: "Oracle Cloud Block Volumes — provision and attach block/boot volumes, take and copy backups, and schedule them with backup policies"},
	"oracle/loadbalancer":          {Name: "Load Balancer", Icon: "circle-nodes", Description: "Oracle Cloud Load Balancer — provision Layer-7 load balancers, wire backend sets and listeners, manage SSL certificates, hostnames and routing, and track backend health"},
	"oracle/networkloadbalancer":   {Name: "Network Load Balancer", Icon: "ethernet", Description: "Oracle Cloud Network Load Balancer — provision Layer 3/4 (TCP/UDP) load balancers, wire backend sets and listeners, tune health checks, and track backend health"},
	"oracle/dns":                   {Name: "DNS", Icon: "globe", Description: "Oracle Cloud DNS — manage public and private zones and their records, steer traffic with policies, and run private-DNS views, resolvers and TSIG keys"},
	"oracle/identity":              {Name: "Identity", Icon: "shield-halved", Description: "Oracle Cloud Identity (IAM) — users, groups and memberships, policies, compartments, dynamic groups, credentials, tagging, federation and identity domains"},
	"oracle/filestorage":           {Name: "File Storage", Icon: "folder-tree", Description: "Oracle Cloud File Storage — provision NFS file systems and mount targets, wire exports, take and schedule snapshots, replicate across regions, and set quotas"},
	"oracle/vault":                 {Name: "Vault", Icon: "lock", Description: "Oracle Cloud Vault & KMS — manage vaults, master encryption keys and key versions, run crypto operations (encrypt/decrypt/sign/verify), and store, rotate and retrieve secrets"},
	"oracle/notifications":         {Name: "Notifications", Icon: "bell", Description: "Oracle Cloud Notifications (ONS) — create topics, manage subscriptions across email, SMS, HTTPS, Slack and more, and publish a message that fans out to every subscriber"},
	"oracle/containerengine":       {Name: "Container Engine", Icon: "cubes", Description: "Oracle Cloud Container Engine for Kubernetes (OKE) — provision and manage clusters, node pools and virtual node pools, install add-ons, generate kubeconfig, rotate credentials, and track asynchronous work requests"},
	"oracle/exadata":               {Name: "Exadata", Icon: "microchip", Description: "Oracle Cloud Exadata Database Service on Dedicated Infrastructure — provision and manage cloud Exadata infrastructure and VM clusters, inspect DB servers and nodes, and schedule maintenance runs"},
	"oracle/streaming":             {Name: "Streaming", Icon: "tower-broadcast", Description: "Oracle Cloud Streaming — a Kafka-compatible managed streaming service: create and manage streams, stream pools and connect harnesses, publish messages, and consume them with cursors and consumer groups"},
	"oracle/queue":                 {Name: "Queue", Icon: "list", Description: "Oracle Cloud Queue — a lightweight managed message queue: create and manage queues, then put, get, delete and update messages with visibility timeouts, channels and dead-letter handling"},
	"oracle/functions":             {Name: "Functions", Icon: "code", Description: "Oracle Cloud Functions — a serverless platform: create and manage applications and their functions, browse pre-built functions, and invoke a function on demand with a payload"},
	"oracle/monitoring":            {Name: "Monitoring", Icon: "gauge", Description: "Oracle Cloud Monitoring — query metrics with the Monitoring Query Language, publish custom metrics, and create alarms that watch a metric and fire to a destination, with status, history and suppressions"},
	"oracle/events":                {Name: "Events", Icon: "bolt", Description: "Oracle Cloud Events — create rules that match OCI event types (a resource created, updated or deleted) with a condition and fan them out to a stream, topic or function"},
	"oracle/logging":               {Name: "Logging", Icon: "file-lines", Description: "Oracle Cloud Logging — manage log groups and logs (service and custom), configure the unified monitoring agent, and search log content across your tenancy"},
	"oracle/apigateway":            {Name: "API Gateway", Icon: "route", Description: "Oracle Cloud API Gateway — publish and manage API gateways and their deployments, wire routes to backends, and version the API specifications they serve"},
	"oracle/waf":                   {Name: "Web Application Firewall", Icon: "shield-virus", Description: "Oracle Cloud Web Application Firewall — protect applications with web-app firewalls and reusable policies, manage network address lists, and browse protection capabilities"},
	"oracle/certificates":          {Name: "Certificates", Icon: "id-badge", Description: "Oracle Cloud Certificates — issue and manage TLS certificates and certificate authorities, bundle CA chains, rotate and revoke versions, and read the issued bundles"},
	"oracle/email":                 {Name: "Email", Icon: "envelope", Description: "Oracle Cloud Email Delivery — manage sending domains and DKIM signing, approved senders, and the suppression list for reliable transactional email"},
	"oracle/nosql":                 {Name: "NoSQL Database", Icon: "table", Description: "Oracle Cloud NoSQL Database — create and manage tables and indexes, read, update and delete rows, and run SQL queries against a fully managed NoSQL store"},
	"oracle/mysql":                 {Name: "MySQL HeatWave", Icon: "database", Description: "Oracle Cloud MySQL HeatWave — provision and manage MySQL DB systems, take and manage backups, tune configurations, and run the in-memory HeatWave analytics cluster"},
	"oracle/dataflow":              {Name: "Data Flow", Icon: "diagram-project", Description: "Oracle Cloud Data Flow — a managed Apache Spark service: define applications, launch and manage runs, wire private endpoints, and submit interactive statements"},
	"oracle/datacatalog":           {Name: "Data Catalog", Icon: "book", Description: "Oracle Cloud Data Catalog — organise data assets and connections, harvest entities, and build a business glossary of terms across your data estate"},
	"oracle/generativeai":          {Name: "Generative AI", Icon: "robot", Description: "Oracle Cloud Generative AI — run chat, text generation, summarization, embeddings and reranking against pretrained and custom large language models, and manage endpoints and dedicated AI clusters"},
	"oracle/language":              {Name: "Language", Icon: "comments", Description: "Oracle Cloud Language — detect language, sentiment, entities, key phrases and PII, classify and translate text, and manage custom language projects, models and endpoints"},
	"oracle/vision":                {Name: "Vision", Icon: "eye", Description: "Oracle Cloud Vision — analyze images and documents for objects, text (OCR), classification and more, and manage vision projects and models"},
	"oracle/documentunderstanding": {Name: "Document Understanding", Icon: "image", Description: "Oracle Cloud Document Understanding — extract text, tables, key-values and classifications from documents, run processor jobs, and manage projects and models"},
	"oracle/speech":                {Name: "Speech", Icon: "microphone", Description: "Oracle Cloud Speech — transcribe audio to text with transcription jobs, synthesize speech, manage custom vocabularies, and list available voices"},
	"oracle/bastion":               {Name: "Bastion", Icon: "terminal", Description: "Oracle Cloud Bastion — create bastions and managed SSH sessions for secure, time-limited access to private hosts without exposing them to the internet"},
	"oracle/waa":                   {Name: "Web Application Acceleration", Icon: "bolt", Description: "Oracle Cloud Web Application Acceleration — speed up web apps with edge caching and compression policies on a load balancer, and purge the cache on demand"},
	"oracle/vulnerabilityscanning": {Name: "Vulnerability Scanning", Icon: "magnifying-glass", Description: "Oracle Cloud Vulnerability Scanning — scan hosts and container images for vulnerabilities and CIS benchmark compliance with reusable recipes and targets, and read the results"},
	"oracle/cloudguard":            {Name: "Cloud Guard", Icon: "shield-halved", Description: "Oracle Cloud Guard — monitor your tenancy's security posture with detector and responder recipes, targets and managed lists, and triage the problems Cloud Guard surfaces"},
	"vectordatabase/azureaisearch": {Name: "Azure AI Search", Icon: "magnifying-glass", Description: "Azure AI Search — manage indexes and documents, and run keyword, vector, and hybrid queries"},
}

// subSubCategoryMetadata maps 3-segment sub-paths (e.g. "crm/apollo/enrichment")
// to display metadata for the third grouping level, used by 4-segment action IDs
// like "crm/apollo/enrichment/people_match". Mirrors the executor category.go
// files under actions/crm/apollo/<type>/.
var subSubCategoryMetadata = map[string]struct {
	Name        string
	Icon        string
	Description string
}{
	"crm/apollo/enrichment": {Name: "Enrichment", Icon: "bolt", Description: "Enrich people and companies with Apollo's data"},
	"crm/apollo/search":     {Name: "Search", Icon: "magnifying-glass", Description: "Search Apollo's people and company database"},
	"crm/apollo/contacts":   {Name: "Contacts", Icon: "user", Description: "Create, update and search Apollo CRM contacts"},
	"crm/apollo/accounts":   {Name: "Accounts", Icon: "briefcase", Description: "Create, update and search Apollo CRM accounts"},
	"crm/apollo/deals":      {Name: "Deals", Icon: "dollar-sign", Description: "Create, update and list Apollo CRM deals (opportunities)"},
	"crm/apollo/sequences":  {Name: "Sequences", Icon: "paper-plane", Description: "Manage Apollo sequences, tasks and engagement"},
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

	// For 4+ segment action IDs, populate sub-sub-category fields (third level)
	if len(parts) >= 4 {
		subSubPath := parts[0] + "/" + parts[1] + "/" + parts[2]
		if subSub, ok := subSubCategoryMetadata[subSubPath]; ok {
			cat.SubSubKey = subSubPath
			cat.SubSubName = subSub.Name
			cat.SubSubIcon = subSub.Icon
			cat.SubSubDescription = subSub.Description
		} else {
			// Auto-generate from directory name
			cat.SubSubKey = subSubPath
			cat.SubSubName = strings.ToUpper(parts[2][:1]) + parts[2][1:]
		}
	}

	return &cat
}

// dynamicOptionsMetadata maps "actionID#inputName" to a dynamic-options
// source injected at serve time — the same in-code-override pattern as
// categoryMetadata above. The input's static Options from the manifest
// remain the editor's fallback when the fetch fails, so entries here must
// point at endpoints returning the same {"options": [{name, value}]} shape.
//
// Not every marker is spelled out below. Where a provider's dropdowns follow
// one regular rule across dozens of actions, they are registered into this map
// from an init() in that provider's own options file rather than listed here as
// a wall of near-identical literals — see kubernetes_options.go (~120 markers)
// and pgvector_options.go (~50). Grep the endpoint name to find them.
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
	// WooCommerce product taxonomy pickers. The Get Many Products "Category" and
	// "Tag" filters each resolve from a proxy that forwards the node's store url /
	// consumer key / secret and lists the store's taxonomy server-side (the key
	// pair are secrets resolved from the environment — the plaintext never
	// transits the browser). The static (empty) options remain the fallback for
	// manual entry when the fetch fails.
	"ecommerce/woocommerce/product_get_all#category": woocommerceCategoriesOption,
	"ecommerce/woocommerce/product_get_all#tag":      woocommerceTagsOption,
	// WordPress live dropdowns. Authors (single-value) get a picker on every
	// place a post/page/comment author is chosen; categories/tags get pickers on
	// the single-value filter and parent-category inputs (the multi-value
	// categories/tags on create/update stay comma-separated — the editor renders
	// dynamic options as a single select).
	"cms/wordpress/post_create#author":      wordpressAuthorsOption,
	"cms/wordpress/post_update#author":      wordpressAuthorsOption,
	"cms/wordpress/post_get_all#author":     wordpressAuthorsOption,
	"cms/wordpress/page_create#author":      wordpressAuthorsOption,
	"cms/wordpress/page_update#author":      wordpressAuthorsOption,
	"cms/wordpress/page_get_all#author":     wordpressAuthorsOption,
	"cms/wordpress/comment_create#author":   wordpressAuthorsOption,
	"cms/wordpress/post_get_all#categories": wordpressCategoriesOption,
	"cms/wordpress/post_get_all#tags":       wordpressTagsOption,
	"cms/wordpress/category_create#parent":  wordpressCategoriesOption,
	"cms/wordpress/category_update#parent":  wordpressCategoriesOption,
	"cms/wordpress/category_get_all#parent": wordpressCategoriesOption,
	// Jira live dropdowns. Issue create/update pull project/issue-type/priority/
	// user pickers from the site; the status picker resolves the selected issue's
	// available transitions. The user pickers also back the user get/delete
	// account_id inputs.
	"jira/issue_create#project":    jiraProjectsOption,
	"jira/issue_create#issue_type": jiraIssueTypesOption,
	"jira/issue_create#priority":   jiraPrioritiesOption,
	"jira/issue_create#assignee":   jiraUsersOption,
	"jira/issue_create#reporter":   jiraUsersOption,
	"jira/issue_update#priority":   jiraPrioritiesOption,
	"jira/issue_update#assignee":   jiraUsersOption,
	"jira/issue_update#reporter":   jiraUsersOption,
	"jira/issue_update#status":     jiraStatusesOption,
	"jira/user_get#account_id":     jiraUsersOption,
	"jira/user_delete#account_id":  jiraUsersOption,
	// Trello live dropdowns. The Boards picker has no dependency; the Lists,
	// Labels and Members pickers each depend on a chosen board (forwarded as
	// board_id). Both credentials are secrets resolved from the environment.
	"trello/board_get#id":                    trelloBoardsOption,
	"trello/board_update#id":                 trelloBoardsOption,
	"trello/board_delete#id":                 trelloBoardsOption,
	"trello/board_member_get_all#board_id":   trelloBoardsOption,
	"trello/board_member_add#board_id":       trelloBoardsOption,
	"trello/board_member_add#member_id":      trelloMembersOption,
	"trello/board_member_invite#board_id":    trelloBoardsOption,
	"trello/board_member_remove#board_id":    trelloBoardsOption,
	"trello/board_member_remove#member_id":   trelloMembersOption,
	"trello/card_create#board_id":            trelloBoardsOption,
	"trello/card_create#list_id":             trelloListsOption,
	"trello/card_update#board_id":            trelloBoardsOption,
	"trello/card_update#list_id":             trelloListsOption,
	"trello/list_create#board_id":            trelloBoardsOption,
	"trello/list_get_all#board_id":           trelloBoardsOption,
	"trello/list_get#board_id":               trelloBoardsOption,
	"trello/list_get#id":                     trelloListsOption,
	"trello/list_update#board_id":            trelloBoardsOption,
	"trello/list_update#id":                  trelloListsOption,
	"trello/list_archive#board_id":           trelloBoardsOption,
	"trello/list_archive#id":                 trelloListsOption,
	"trello/list_get_cards#board_id":         trelloBoardsOption,
	"trello/list_get_cards#id":               trelloListsOption,
	"trello/label_create#board_id":           trelloBoardsOption,
	"trello/label_get_all#board_id":          trelloBoardsOption,
	"trello/label_get#board_id":              trelloBoardsOption,
	"trello/label_get#id":                    trelloLabelsOption,
	"trello/label_update#board_id":           trelloBoardsOption,
	"trello/label_update#id":                 trelloLabelsOption,
	"trello/label_delete#board_id":           trelloBoardsOption,
	"trello/label_delete#id":                 trelloLabelsOption,
	"trello/label_add_to_card#board_id":      trelloBoardsOption,
	"trello/label_add_to_card#label_id":      trelloLabelsOption,
	"trello/label_remove_from_card#board_id": trelloBoardsOption,
	"trello/label_remove_from_card#label_id": trelloLabelsOption,
	// The webhook trigger watches one model; the Boards picker covers the common
	// case (watch a whole board). Operators can still paste a list/card id.
	"trigger/trello_webhook#model_id": trelloBoardsOption,
	// Asana live dropdowns. Workspaces has no dependency; Projects, Users, Tags
	// and Teams depend on a chosen workspace; Sections depends on a chosen
	// project. The access token is a secret resolved from the environment.
	"asana/task_create#workspace":       asanaWorkspacesOption,
	"asana/task_create#assignee":        asanaUsersOption,
	"asana/task_create#projects":        asanaProjectsOption,
	"asana/task_update#assignee":        asanaUsersOption,
	"asana/task_get_all#workspace":      asanaWorkspacesOption,
	"asana/task_get_all#project":        asanaProjectsOption,
	"asana/task_get_all#assignee":       asanaUsersOption,
	"asana/task_get_all#section":        asanaSectionsOption,
	"asana/task_search#workspace":       asanaWorkspacesOption,
	"asana/task_move#project_id":        asanaProjectsOption,
	"asana/task_move#section":           asanaSectionsOption,
	"asana/task_add_project#project":    asanaProjectsOption,
	"asana/task_remove_project#project": asanaProjectsOption,
	"asana/task_add_tag#workspace":      asanaWorkspacesOption,
	"asana/task_add_tag#tag":            asanaTagsOption,
	"asana/task_remove_tag#workspace":   asanaWorkspacesOption,
	"asana/task_remove_tag#tag":         asanaTagsOption,
	"asana/user_get_all#workspace":      asanaWorkspacesOption,
	"asana/project_create#workspace":    asanaWorkspacesOption,
	"asana/project_create#team":         asanaTeamsOption,
	"asana/project_get_all#workspace":   asanaWorkspacesOption,
	"asana/project_get_all#team":        asanaTeamsOption,
	"asana/project_update#owner":        asanaUsersOption,
	"asana/section_create#project_id":   asanaProjectsOption,
	"asana/section_get_all#project_id":  asanaProjectsOption,
	"asana/tag_create#workspace":        asanaWorkspacesOption,
	"asana/tag_get_all#workspace":       asanaWorkspacesOption,
	"asana/subtask_create#assignee":     asanaUsersOption,
	"asana/workspace_get_all#workspace": asanaWorkspacesOption,
	"trigger/asana_webhook#workspace":   asanaWorkspacesOption,
	"trigger/asana_webhook#resource":    asanaProjectsOption,
	// Monday.com live dropdowns. Boards & Workspaces have no dependency; Groups
	// and Columns depend on a chosen board (forwarded as board_id). The API token
	// is a secret resolved from the environment.
	"monday/board_get#board_id":                          mondayBoardsOption,
	"monday/board_archive#board_id":                      mondayBoardsOption,
	"monday/board_create#workspace_id":                   mondayWorkspacesOption,
	"monday/board_column_create#board_id":                mondayBoardsOption,
	"monday/board_column_get_all#board_id":               mondayBoardsOption,
	"monday/board_group_create#board_id":                 mondayBoardsOption,
	"monday/board_group_delete#board_id":                 mondayBoardsOption,
	"monday/board_group_delete#group_id":                 mondayGroupsOption,
	"monday/board_group_get_all#board_id":                mondayBoardsOption,
	"monday/board_group_update#board_id":                 mondayBoardsOption,
	"monday/board_group_update#group_id":                 mondayGroupsOption,
	"monday/item_create#board_id":                        mondayBoardsOption,
	"monday/item_create#group_id":                        mondayGroupsOption,
	"monday/item_get_all#board_id":                       mondayBoardsOption,
	"monday/item_get_all#group_id":                       mondayGroupsOption,
	"monday/item_get_by_column_value#board_id":           mondayBoardsOption,
	"monday/item_get_by_column_value#column_id":          mondayColumnsOption,
	"monday/item_change_column_value#board_id":           mondayBoardsOption,
	"monday/item_change_column_value#column_id":          mondayColumnsOption,
	"monday/item_change_multiple_column_values#board_id": mondayBoardsOption,
	"monday/item_move#board_id":                          mondayBoardsOption,
	"monday/item_move#group_id":                          mondayGroupsOption,
	"trigger/monday_webhook#board_id":                    mondayBoardsOption,
	// Intercom live dropdowns. Teammates (admins), teams, tags, ticket types,
	// ticket states, segments, companies, and Help Center collections are all
	// workspace-scoped lists with no dependency inputs, so every picker resolves
	// from the same eight proxies. The access token is a secret resolved from the
	// environment.
	"helpdesk/intercom/contact_create#owner_id":                       intercomAdminsOption,
	"helpdesk/intercom/contact_update#owner_id":                       intercomAdminsOption,
	"helpdesk/intercom/contact_tag_add#tag_id":                        intercomTagsOption,
	"helpdesk/intercom/contact_tag_remove#tag_id":                     intercomTagsOption,
	"helpdesk/intercom/company_get_all#tag_id":                        intercomTagsOption,
	"helpdesk/intercom/company_get_all#segment_id":                    intercomSegmentsOption,
	"helpdesk/intercom/company_contact_attach#company_id":             intercomCompaniesOption,
	"helpdesk/intercom/company_contact_detach#company_id":             intercomCompaniesOption,
	"helpdesk/intercom/company_tag_add#company_id":                    intercomCompaniesOption,
	"helpdesk/intercom/company_tag_remove#company_id":                 intercomCompaniesOption,
	"helpdesk/intercom/conversation_reply#admin_id":                   intercomAdminsOption,
	"helpdesk/intercom/conversation_close#admin_id":                   intercomAdminsOption,
	"helpdesk/intercom/conversation_snooze#admin_id":                  intercomAdminsOption,
	"helpdesk/intercom/conversation_open#admin_id":                    intercomAdminsOption,
	"helpdesk/intercom/conversation_assign#admin_id":                  intercomAdminsOption,
	"helpdesk/intercom/conversation_assign#assignee_admin_id":         intercomAdminsOption,
	"helpdesk/intercom/conversation_assign#assignee_team_id":          intercomTeamsOption,
	"helpdesk/intercom/conversation_convert_to_ticket#ticket_type_id": intercomTicketTypesOption,
	"helpdesk/intercom/conversation_tag_add#tag_id":                   intercomTagsOption,
	"helpdesk/intercom/conversation_tag_add#admin_id":                 intercomAdminsOption,
	"helpdesk/intercom/conversation_tag_remove#tag_id":                intercomTagsOption,
	"helpdesk/intercom/conversation_tag_remove#admin_id":              intercomAdminsOption,
	"helpdesk/intercom/ticket_create#ticket_type_id":                  intercomTicketTypesOption,
	"helpdesk/intercom/ticket_create#admin_assignee_id":               intercomAdminsOption,
	"helpdesk/intercom/ticket_create#team_assignee_id":                intercomTeamsOption,
	"helpdesk/intercom/ticket_update#ticket_state_id":                 intercomTicketStatesOption,
	"helpdesk/intercom/ticket_update#admin_id":                        intercomAdminsOption,
	"helpdesk/intercom/ticket_reply#admin_id":                         intercomAdminsOption,
	"helpdesk/intercom/ticket_tag_add#tag_id":                         intercomTagsOption,
	"helpdesk/intercom/ticket_tag_add#admin_id":                       intercomAdminsOption,
	"helpdesk/intercom/ticket_tag_remove#tag_id":                      intercomTagsOption,
	"helpdesk/intercom/ticket_tag_remove#admin_id":                    intercomAdminsOption,
	"helpdesk/intercom/tag_delete#tag_id":                             intercomTagsOption,
	"helpdesk/intercom/note_create#admin_id":                          intercomAdminsOption,
	"helpdesk/intercom/segment_get#segment_id":                        intercomSegmentsOption,
	"helpdesk/intercom/admin_get#admin_id":                            intercomAdminsOption,
	"helpdesk/intercom/admin_away_set#admin_id":                       intercomAdminsOption,
	"helpdesk/intercom/team_get#team_id":                              intercomTeamsOption,
	"helpdesk/intercom/message_send#from_admin_id":                    intercomAdminsOption,
	"helpdesk/intercom/article_create#author_id":                      intercomAdminsOption,
	"helpdesk/intercom/article_create#parent_id":                      intercomCollectionsOption,
	"helpdesk/intercom/article_update#author_id":                      intercomAdminsOption,
	"helpdesk/intercom/article_update#parent_id":                      intercomCollectionsOption,
	// SendGrid live dropdowns. Contact lists, dynamic templates, unsubscribe
	// (ASM) groups, and segments are all account-scoped lists with no dependency
	// inputs, so every picker resolves from the same four proxies. The API key is
	// a secret resolved from the environment.
	"marketing/sendgrid/list_get#list_id":                      sendgridListsOption,
	"marketing/sendgrid/list_update#list_id":                   sendgridListsOption,
	"marketing/sendgrid/list_delete#list_id":                   sendgridListsOption,
	"marketing/sendgrid/list_remove_contacts#list_id":          sendgridListsOption,
	"marketing/sendgrid/mail_send#template_id":                 sendgridTemplatesOption,
	"marketing/sendgrid/template_get#template_id":              sendgridTemplatesOption,
	"marketing/sendgrid/template_update#template_id":           sendgridTemplatesOption,
	"marketing/sendgrid/template_delete#template_id":           sendgridTemplatesOption,
	"marketing/sendgrid/template_version_create#template_id":   sendgridTemplatesOption,
	"marketing/sendgrid/template_version_activate#template_id": sendgridTemplatesOption,
	"marketing/sendgrid/mail_send#asm_group_id":                sendgridAsmGroupsOption,
	"marketing/sendgrid/asm_group_get#group_id":                sendgridAsmGroupsOption,
	"marketing/sendgrid/asm_group_update#group_id":             sendgridAsmGroupsOption,
	"marketing/sendgrid/asm_group_delete#group_id":             sendgridAsmGroupsOption,
	"marketing/sendgrid/asm_suppression_add#group_id":          sendgridAsmGroupsOption,
	"marketing/sendgrid/asm_suppression_list#group_id":         sendgridAsmGroupsOption,
	"marketing/sendgrid/asm_suppression_delete#group_id":       sendgridAsmGroupsOption,
	"marketing/sendgrid/segment_get#segment_id":                sendgridSegmentsOption,
}

// Jira live-dropdown markers. Every Jira action forwards the same connection
// inputs (site url + email plain, api_token a secret resolved from the
// environment — the plaintext never transits the browser). "environment" is
// listed explicitly so a ${secrets.X} API token resolves server-side (the editor
// also auto-appends it, but this guarantees it). The issue-type and status
// pickers additionally forward the dependency field they resolve against (the
// selected project / issue). The static (empty) options remain the fallback for
// manual entry when the fetch fails.
var jiraProjectsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/jira-projects",
	Params:   []string{"url", "email", "api_token", "environment"},
}
var jiraIssueTypesOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/jira-issue-types",
	Params:   []string{"url", "email", "api_token", "project", "environment"},
}
var jiraPrioritiesOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/jira-priorities",
	Params:   []string{"url", "email", "api_token", "environment"},
}
var jiraUsersOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/jira-users",
	Params:   []string{"url", "email", "api_token", "environment"},
}
var jiraStatusesOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/jira-statuses",
	Params:   []string{"url", "email", "api_token", "issue_key", "environment"},
}

// Trello live-dropdown markers. Every Trello action forwards the same
// credentials (api_key + api_token, BOTH secrets resolved from the environment —
// the plaintext never transits the browser). "environment" is listed explicitly
// so the ${secrets.X} references resolve server-side (the editor also
// auto-appends it, but this guarantees it). The Lists/Labels/Members pickers
// additionally forward the selected board (board_id) they resolve against. The
// static (empty) options remain the fallback for manual entry on fetch failure.
var trelloBoardsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/trello-boards",
	Params:   []string{"api_key", "api_token", "environment"},
}
var trelloListsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/trello-lists",
	Params:   []string{"api_key", "api_token", "board_id", "environment"},
}
var trelloLabelsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/trello-labels",
	Params:   []string{"api_key", "api_token", "board_id", "environment"},
}
var trelloMembersOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/trello-members",
	Params:   []string{"api_key", "api_token", "board_id", "environment"},
}

// Asana live-dropdown markers. Every Asana action forwards the same credential
// (access_token, a secret resolved from the environment — the plaintext never
// transits the browser). "environment" is listed explicitly so the ${secrets.X}
// reference resolves server-side. Dependent pickers additionally forward their
// scope (workspace for projects/users/tags/teams; project_id/project for
// sections). The users picker forwards workspace optionally (GET /users works
// without it), so it can back assignee/owner fields on actions with no workspace.
var asanaWorkspacesOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/asana-workspaces",
	Params:   []string{"access_token", "environment"},
}
var asanaProjectsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/asana-projects",
	Params:   []string{"access_token", "workspace", "environment"},
}
var asanaUsersOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/asana-users",
	Params:   []string{"access_token", "workspace", "environment"},
}
var asanaSectionsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/asana-sections",
	Params:   []string{"access_token", "project_id", "project", "environment"},
}
var asanaTagsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/asana-tags",
	Params:   []string{"access_token", "workspace", "environment"},
}
var asanaTeamsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/asana-teams",
	Params:   []string{"access_token", "workspace", "environment"},
}

// Monday.com live-dropdown markers. Every Monday action forwards the same
// credential (api_token, a secret resolved from the environment). The Groups and
// Columns pickers additionally forward the selected board (board_id) they resolve
// against.
var mondayBoardsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/monday-boards",
	Params:   []string{"api_token", "environment"},
}
var mondayGroupsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/monday-groups",
	Params:   []string{"api_token", "board_id", "environment"},
}
var mondayColumnsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/monday-columns",
	Params:   []string{"api_token", "board_id", "environment"},
}
var mondayWorkspacesOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/monday-workspaces",
	Params:   []string{"api_token", "environment"},
}

// Intercom live-dropdown markers. Every Intercom action forwards the same
// connection inputs (api_token, a secret resolved from the environment — the
// plaintext never transits the browser — plus the plain Region picker that
// selects the fixed regional host). "environment" is listed explicitly so the
// ${secrets.X} reference resolves server-side. None of the pickers depend on
// another input — every Intercom list is workspace-scoped.
var intercomAdminsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/intercom-admins",
	Params:   []string{"api_token", "region", "environment"},
}
var intercomTeamsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/intercom-teams",
	Params:   []string{"api_token", "region", "environment"},
}
var intercomTagsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/intercom-tags",
	Params:   []string{"api_token", "region", "environment"},
}
var intercomTicketTypesOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/intercom-ticket-types",
	Params:   []string{"api_token", "region", "environment"},
}
var intercomTicketStatesOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/intercom-ticket-states",
	Params:   []string{"api_token", "region", "environment"},
}
var intercomSegmentsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/intercom-segments",
	Params:   []string{"api_token", "region", "environment"},
}
var intercomCompaniesOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/intercom-companies",
	Params:   []string{"api_token", "region", "environment"},
}
var intercomCollectionsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/intercom-collections",
	Params:   []string{"api_token", "region", "environment"},
}

// SendGrid live-dropdown markers. Every SendGrid action forwards the same
// connection inputs (api_key, a secret resolved from the environment — the
// plaintext never transits the browser — plus the plain Region picker that
// selects the fixed Global/EU host). "environment" is listed explicitly so the
// ${secrets.X} reference resolves server-side. None of the pickers depend on
// another input — every SendGrid list is account-scoped.
var sendgridListsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/sendgrid-lists",
	Params:   []string{"api_key", "region", "environment"},
}
var sendgridTemplatesOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/sendgrid-templates",
	Params:   []string{"api_key", "region", "environment"},
}
var sendgridAsmGroupsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/sendgrid-asm-groups",
	Params:   []string{"api_key", "region", "environment"},
}
var sendgridSegmentsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/sendgrid-segments",
	Params:   []string{"api_key", "region", "environment"},
}

// jenkinsJobsOption is the shared dynamic-options marker for every Jenkins
// "Job" input — declared once so the 11 entries above stay identical.
var jenkinsJobsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/jenkins-jobs",
	Params:   []string{"base_url", "username", "api_token"},
}

// woocommerceCategoriesOption / woocommerceTagsOption are the shared
// dynamic-options markers for the WooCommerce product-taxonomy pickers. Both
// forward the WooCommerce connection inputs; the api resolves the secret key
// pair from the environment before calling the store. "environment" is listed
// explicitly (the editor also auto-appends it for any marker with params, but
// making it explicit guarantees serveWooCommerceOptions can resolve a
// ${secrets.X} consumer key/secret rather than erroring).
var woocommerceCategoriesOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/woocommerce-categories",
	Params:   []string{"url", "consumer_key", "consumer_secret", "credentials_in_query", "environment"},
}
var woocommerceTagsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/woocommerce-tags",
	Params:   []string{"url", "consumer_key", "consumer_secret", "credentials_in_query", "environment"},
}

// WordPress live-dropdown markers. Every WordPress action forwards the same
// connection inputs; the api resolves the Application Password from the
// environment before calling the site. "environment" is listed explicitly so a
// ${secrets.X} Application Password resolves server-side.
var wordpressAuthorsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/wordpress-authors",
	Params:   []string{"url", "username", "app_password", "allow_insecure", "environment"},
}
var wordpressCategoriesOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/wordpress-categories",
	Params:   []string{"url", "username", "app_password", "allow_insecure", "environment"},
}
var wordpressTagsOption = api.InputDynamicOptions{
	Endpoint: "/api/v1/action/options/wordpress-tags",
	Params:   []string{"url", "username", "app_password", "allow_insecure", "environment"},
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
				} else if dyn, ok := awsDynamicOption(a.ID, inputs[idx].Name); ok {
					// Rule-based: any aws/* action's resource inputs get the
					// matching live picker (see aws_options.go).
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
