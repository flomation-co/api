package http

// Dynamic-options markers for the CRM ▸ Salesforce actions.
//
// 565 markers over 177 actions, registered from a table in init() rather than
// spelled out as literals in action.go. action.go sanctions exactly this — see
// the comment above dynamicOptionsMetadata — and kubernetes_options.go (~120
// markers) and pgvector_options.go (~50) do the same. Package-level variables
// are initialised before any init() runs, so dynamicOptionsMetadata is non-nil
// here.
//
// Reading the table:
//
//	{input, endpoint, extraParams, actions}
//
//   - input is the action input the picker fills.
//   - endpoint is the proxy slug plus any query the marker BAKES IN. Where the
//     object is fixed by the action (account_create can only ever touch Account)
//     it is baked; where the action has its own object input it is forwarded
//     instead, and the endpoint carries no object.
//   - extraParams are the sibling inputs the editor forwards on top of the auth
//     pair — "object" / "custom_object" / "link_to_object" for the generic
//     actions, "campaign_id" for the two-hop member-status picker, and
//     "quote_id" / "order_id" / "opportunity_id" / "product_id" for the two-hop
//     price book entry picker.
//   - actions are the action ids under crm/salesforce/.
//
// EVERY marker's Params carry access_token, instance_url AND environment.
// Dropping environment is not cosmetic: the token usually arrives as a
// ${secrets.X} or ${credentials.X} reference, and without the environment id the
// api cannot resolve it — the picker would then die for every operator who is
// not pasting a raw token, which is nearly all of them.
//
// Every (action, input) pair below was checked against the action packages'
// literal Inputs arrays: a marker whose input does not exist is dead weight, and
// an input that should have a picker and does not is worse.
//
// Deliberately NOT registered:
//
//   - record_merge#object, email_message_create#status, campaign_member_get_all
//     #member_type, account_describe#detail_level — these carry curated static
//     options that are narrower than the org's own list (Salesforce only merges
//     Account/Contact/Lead), so a live list would offer choices the action
//     rejects.
//   - record_upsert#fields — a JSON object of field→value, not a field name.
//   - record_get_related#fields — the fields belong to the CHILD object named by
//     relationship_name, which no picker here can resolve.
//   - search_records#fields — its objects input is a LIST, so there is no single
//     object whose fields to show.
//   - comment_id, contact_role_id, relation_id, work_item_id — CaseComment,
//     OpportunityContactRole, AccountContactRelation and ProcessInstanceWorkitem
//     have no name field, so a picker could only list opaque ids.
//   - record_undelete#record_id — it names a record already in the recycle bin,
//     which an ordinary query does not return.
//
// And from the commerce actions, each for a reason that was checked rather than
// assumed:
//
//   - quote_create / quote_update#quote_status and order_create /
//     order_update#order_status — the ACTIONS validate these against a closed list
//     and refuse anything else outright ("is not a Salesforce quote status"), so a
//     live picklist would offer an org's own added statuses and the action would
//     then reject every one. Same reason as email_message_create#status.
//   - product_*#product_type — a curated Base / Bundle / Set. Product2.Type is an
//     UNRESTRICTED picklist that ships with no active values in some orgs, so a
//     live picker there would replace three choices Salesforce accepts with "there
//     are no set choices — type the value in". Strictly worse.
//   - billing_state / shipping_state / state — with the org's State and Country
//     picklists on, StateCode is a DEPENDENT picklist controlled by CountryCode
//     (384 values spanning every country, verified live). /salesforce-picklist has
//     no controlling-field support, so it would offer every state of every country
//     and the operator would have to find theirs in the middle of it.
//   - billing_country / shipping_country / country — the actions write the NAME
//     field (BillingCountry), which Salesforce validates against the picklist's
//     integration values, while the describe's picklist is on the CODE field and
//     carries two-letter codes. A picker here would commit "GB" into a field that
//     wants "United Kingdom".
//   - price_book_entry_get_all#product_code, asset_*#serial_number,
//     product_upsert#match_value — filter VALUES typed by the operator, not record
//     names or picklists. Nothing enumerates them.
//   - filter_operator — the closed set of SOQL comparison operators, which is our
//     vocabulary and not the org's.
//   - additional_fields, filter_conditions — JSON payloads, same as
//     record_upsert#fields.
//
// Note on the multi-value inputs (`fields`, `objects`): the editor renders
// dynamic options as a SINGLE select, so choosing an option replaces a
// comma-separated list rather than appending to it. They are still registered —
// seeing the org's real API names is the whole difficulty for a non-technical
// operator, and the dropdown's free-text row still commits a typed list verbatim.

import "flomation.app/automate/api"

// salesforcePickerGroup is one endpoint/input pairing and the actions it covers.
type salesforcePickerGroup struct {
	Input    string
	Endpoint string
	Extra    []string
	Actions  []string
}

// salesforceAuthParams are forwarded on every Salesforce option fetch.
// environment is listed explicitly rather than left to the editor's implicit
// append, so the requirement is visible at the point it matters.
var salesforceAuthParams = []string{"access_token", "instance_url", "environment"}

// salesforcePickerGroups is the whole marker table. Ordered by endpoint family:
// objects, fields, picklists, record types, external ids, lookups, users,
// owners, then the one-off pickers.
var salesforcePickerGroups = []salesforcePickerGroup{
	// Salesforce Object pickers — the global describe.
	{"object", "salesforce-objects", nil, []string{"approval_get_all", "approval_submit", "attachment_create", "email_message_create", "file_get_all_for_record", "file_link_to_record", "list_view_get_all", "list_view_run", "note_create", "note_get_all_for_record", "object_describe", "quick_action_get_all", "quick_action_run", "record_create_many", "record_find", "record_get_deleted", "record_get_related", "record_get_updated", "record_update_many", "record_upsert", "record_upsert_many", "task_get_all_for_record"}},
	// file_upload names its attach-to object under a different input.
	{"link_to_object", "salesforce-objects", nil, []string{"file_upload"}},
	// search_records takes a comma-separated object list.
	{"objects", "salesforce-objects", nil, []string{"search_records"}},
	// Custom-object pickers: the same describe, filtered to custom objects.
	{"custom_object", "salesforce-objects?custom_only=true", nil, []string{"custom_object_create", "custom_object_delete", "custom_object_get", "custom_object_get_all", "custom_object_update", "custom_object_upsert"}},
	{"fields", "salesforce-fields?filter=all&object=Account", nil, []string{"account_get", "account_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=AccountContactRelation", nil, []string{"account_contact_relation_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=Attachment", nil, []string{"attachment_get", "attachment_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=Campaign", nil, []string{"campaign_get", "campaign_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=CampaignMember", nil, []string{"campaign_member_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=Case", nil, []string{"case_get", "case_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=CaseComment", nil, []string{"case_comment_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=Contact", nil, []string{"contact_get", "contact_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=EmailMessage", nil, []string{"email_message_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=Event", nil, []string{"event_get", "event_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=Lead", nil, []string{"lead_get", "lead_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=Opportunity", nil, []string{"opportunity_get", "opportunity_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=OpportunityContactRole", nil, []string{"opportunity_contact_role_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=OpportunityLineItem", nil, []string{"opportunity_line_item_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=Task", nil, []string{"task_get", "task_get_all", "task_get_all_for_record"}},
	{"fields", "salesforce-fields?filter=all&object=User", nil, []string{"user_get", "user_get_all"}},
	{"fields", "salesforce-fields?filter=all", []string{"custom_object"}, []string{"custom_object_get", "custom_object_get_all"}},
	{"fields", "salesforce-fields?filter=all", []string{"object"}, []string{"record_find"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Account", nil, []string{"account_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Attachment", nil, []string{"attachment_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Campaign", nil, []string{"campaign_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Case", nil, []string{"case_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Contact", nil, []string{"contact_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Event", nil, []string{"event_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Lead", nil, []string{"lead_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Opportunity", nil, []string{"opportunity_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Task", nil, []string{"task_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=User", nil, []string{"user_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable", []string{"custom_object"}, []string{"custom_object_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Account", nil, []string{"account_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=AccountContactRelation", nil, []string{"account_contact_relation_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Attachment", nil, []string{"attachment_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Campaign", nil, []string{"campaign_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=CampaignMember", nil, []string{"campaign_member_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Case", nil, []string{"case_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=CaseComment", nil, []string{"case_comment_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Contact", nil, []string{"contact_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=EmailMessage", nil, []string{"email_message_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Event", nil, []string{"event_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Lead", nil, []string{"lead_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Opportunity", nil, []string{"opportunity_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=OpportunityContactRole", nil, []string{"opportunity_contact_role_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=OpportunityLineItem", nil, []string{"opportunity_line_item_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Task", nil, []string{"task_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=User", nil, []string{"user_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable", []string{"custom_object"}, []string{"custom_object_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable", []string{"object"}, []string{"record_find"}},
	// A quick action sets fields on the record it creates.
	{"field_name", "salesforce-fields?filter=createable", []string{"object"}, []string{"quick_action_run"}},
	{"match_field", "salesforce-fields?filter=filterable", []string{"object"}, []string{"record_find"}},
	// Only picklist fields have values worth describing.
	{"picklist_field", "salesforce-fields?filter=picklist", []string{"object"}, []string{"object_describe"}},
	{"status", "salesforce-picklist?object=Lead&field=Status", nil, []string{"lead_create", "lead_update", "lead_upsert"}},
	// NOT the Lead.Status picklist: only the statuses an administrator has ticked
	// "Converted" convert, and convertLead answers any of the others with
	// INVALID_STATUS. Three of a default org's four would fail.
	{"converted_status", "salesforce-lead-converted-statuses", nil, []string{"lead_convert"}},
	{"lead_source", "salesforce-picklist?object=Contact&field=LeadSource", nil, []string{"contact_create", "contact_update", "contact_upsert"}},
	{"lead_source", "salesforce-picklist?object=Lead&field=LeadSource", nil, []string{"lead_create", "lead_update", "lead_upsert"}},
	{"lead_source", "salesforce-picklist?object=Opportunity&field=LeadSource", nil, []string{"opportunity_create", "opportunity_update", "opportunity_upsert"}},
	{"industry", "salesforce-picklist?object=Account&field=Industry", nil, []string{"account_create", "account_update", "account_upsert"}},
	{"industry", "salesforce-picklist?object=Lead&field=Industry", nil, []string{"lead_create", "lead_update", "lead_upsert"}},
	{"rating", "salesforce-picklist?object=Lead&field=Rating", nil, []string{"lead_create", "lead_update", "lead_upsert"}},
	{"salutation", "salesforce-picklist?object=Contact&field=Salutation", nil, []string{"contact_create", "contact_update", "contact_upsert"}},
	{"salutation", "salesforce-picklist?object=Lead&field=Salutation", nil, []string{"lead_create", "lead_update", "lead_upsert"}},
	{"stage_name", "salesforce-picklist?object=Opportunity&field=StageName", nil, []string{"opportunity_create", "opportunity_update", "opportunity_upsert"}},
	{"opportunity_type", "salesforce-picklist?object=Opportunity&field=Type", nil, []string{"opportunity_create", "opportunity_update", "opportunity_upsert"}},
	{"forecast_category", "salesforce-picklist?object=Opportunity&field=ForecastCategoryName", nil, []string{"opportunity_create", "opportunity_update", "opportunity_upsert"}},
	{"account_type", "salesforce-picklist?object=Account&field=Type", nil, []string{"account_create", "account_update", "account_upsert"}},
	{"account_source", "salesforce-picklist?object=Account&field=AccountSource", nil, []string{"account_create", "account_update", "account_upsert"}},
	{"case_type", "salesforce-picklist?object=Case&field=Type", nil, []string{"case_create", "case_update", "case_upsert"}},
	{"case_status", "salesforce-picklist?object=Case&field=Status", nil, []string{"case_close", "case_create", "case_update", "case_upsert"}},
	{"case_origin", "salesforce-picklist?object=Case&field=Origin", nil, []string{"case_create", "case_update", "case_upsert"}},
	{"case_reason", "salesforce-picklist?object=Case&field=Reason", nil, []string{"case_close", "case_create", "case_update", "case_upsert"}},
	{"priority", "salesforce-picklist?object=Case&field=Priority", nil, []string{"case_create", "case_update", "case_upsert"}},
	{"task_status", "salesforce-picklist?object=Task&field=Status", nil, []string{"task_complete", "task_create", "task_get_all", "task_update", "task_upsert"}},
	{"task_priority", "salesforce-picklist?object=Task&field=Priority", nil, []string{"task_create", "task_get_all", "task_update", "task_upsert"}},
	{"task_type", "salesforce-picklist?object=Task&field=Type", nil, []string{"task_create", "task_update", "task_upsert"}},
	{"task_subtype", "salesforce-picklist?object=Task&field=TaskSubtype", nil, []string{"task_create"}},
	{"call_type", "salesforce-picklist?object=Task&field=CallType", nil, []string{"task_create", "task_update", "task_upsert"}},
	{"recurrence_type", "salesforce-picklist?object=Task&field=RecurrenceType", nil, []string{"task_create", "task_update"}},
	{"recurrence_instance", "salesforce-picklist?object=Task&field=RecurrenceInstance", nil, []string{"task_create", "task_update"}},
	{"recurrence_regenerated_type", "salesforce-picklist?object=Task&field=RecurrenceRegeneratedType", nil, []string{"task_create", "task_update"}},
	{"show_as", "salesforce-picklist?object=Event&field=ShowAs", nil, []string{"event_create", "event_update"}},
	{"event_subtype", "salesforce-picklist?object=Event&field=EventSubtype", nil, []string{"event_create"}},
	{"recurrence_type", "salesforce-picklist?object=Event&field=RecurrenceType", nil, []string{"event_create"}},
	{"recurrence_instance", "salesforce-picklist?object=Event&field=RecurrenceInstance", nil, []string{"event_create"}},
	{"campaign_type", "salesforce-picklist?object=Campaign&field=Type", nil, []string{"campaign_create", "campaign_get_all", "campaign_update"}},
	{"campaign_status", "salesforce-picklist?object=Campaign&field=Status", nil, []string{"campaign_create", "campaign_get_all", "campaign_update"}},
	{"gender_identity", "salesforce-picklist?object=Contact&field=GenderIdentity", nil, []string{"contact_create", "contact_update", "contact_upsert"}},
	{"pronouns", "salesforce-picklist?object=Contact&field=Pronouns", nil, []string{"contact_create", "contact_update", "contact_upsert"}},
	{"role", "salesforce-picklist?object=OpportunityContactRole&field=Role", nil, []string{"opportunity_contact_role_create", "opportunity_contact_role_update"}},
	{"role", "salesforce-picklist?object=AccountContactRelation&field=Roles", nil, []string{"account_contact_relation_get_all"}},
	{"roles", "salesforce-picklist?object=AccountContactRelation&field=Roles", nil, []string{"account_contact_relation_create"}},
	{"visibility", "salesforce-picklist?object=ContentDocumentLink&field=Visibility", nil, []string{"file_link_to_record", "note_create"}},
	{"share_type", "salesforce-picklist?object=ContentDocumentLink&field=ShareType", nil, []string{"file_link_to_record", "note_create"}},
	{"language_locale_key", "salesforce-picklist?object=User&field=LanguageLocaleKey", nil, []string{"user_create", "user_update"}},
	{"locale_sid_key", "salesforce-picklist?object=User&field=LocaleSidKey", nil, []string{"user_create", "user_update"}},
	{"time_zone_sid_key", "salesforce-picklist?object=User&field=TimeZoneSidKey", nil, []string{"user_create", "user_update"}},
	{"email_encoding_key", "salesforce-picklist?object=User&field=EmailEncodingKey", nil, []string{"user_create"}},
	{"division", "salesforce-picklist?object=User&field=Division", nil, []string{"user_create", "user_update"}},
	{"record_type_id", "salesforce-record-types?object=Account", nil, []string{"account_create", "account_update", "account_upsert"}},
	{"record_type_id", "salesforce-record-types?object=Campaign", nil, []string{"campaign_create"}},
	{"record_type_id", "salesforce-record-types?object=Case", nil, []string{"case_create", "case_update", "case_upsert"}},
	{"record_type_id", "salesforce-record-types?object=Contact", nil, []string{"contact_create", "contact_update", "contact_upsert"}},
	{"record_type_id", "salesforce-record-types?object=Event", nil, []string{"event_create"}},
	{"record_type_id", "salesforce-record-types?object=Lead", nil, []string{"lead_create", "lead_update", "lead_upsert"}},
	{"record_type_id", "salesforce-record-types?object=Opportunity", nil, []string{"opportunity_create", "opportunity_update"}},
	{"record_type_id", "salesforce-record-types", []string{"custom_object"}, []string{"custom_object_create", "custom_object_update", "custom_object_upsert"}},
	{"record_type_id", "salesforce-record-types", []string{"object"}, []string{"object_describe", "record_upsert"}},
	{"external_id_field", "salesforce-external-id-fields?object=Account", nil, []string{"account_upsert"}},
	{"external_id_field", "salesforce-external-id-fields?object=Case", nil, []string{"case_upsert"}},
	{"external_id_field", "salesforce-external-id-fields?object=Contact", nil, []string{"contact_upsert"}},
	{"external_id_field", "salesforce-external-id-fields?object=Lead", nil, []string{"lead_upsert"}},
	{"external_id_field", "salesforce-external-id-fields?object=Opportunity", nil, []string{"opportunity_upsert"}},
	{"external_id_field", "salesforce-external-id-fields?object=Task", nil, []string{"task_upsert"}},
	{"external_id_field", "salesforce-external-id-fields", []string{"custom_object"}, []string{"custom_object_upsert"}},
	{"external_id_field", "salesforce-external-id-fields", []string{"object"}, []string{"record_upsert", "record_upsert_many"}},
	{"account_id", "salesforce-lookup?object=Account", nil, []string{"account_add_note", "account_contact_relation_create", "account_contact_relation_get_all", "account_delete", "account_get", "account_update", "case_create", "case_update", "case_upsert", "contact_create", "contact_update", "contact_upsert", "lead_convert", "opportunity_create", "opportunity_update", "opportunity_upsert"}},
	{"contact_id", "salesforce-lookup?object=Contact", nil, []string{"account_contact_relation_create", "account_contact_relation_get_all", "campaign_member_create", "campaign_member_delete", "campaign_member_update", "case_create", "case_update", "case_upsert", "contact_add_note", "contact_add_to_campaign", "contact_delete", "contact_get", "contact_update", "lead_convert", "opportunity_contact_role_create", "opportunity_contact_role_update"}},
	{"lead_id", "salesforce-lookup?object=Lead", nil, []string{"campaign_member_create", "campaign_member_delete", "campaign_member_update", "lead_add_note", "lead_add_to_campaign", "lead_convert", "lead_delete", "lead_get", "lead_update"}},
	{"opportunity_id", "salesforce-lookup?object=Opportunity", nil, []string{"opportunity_add_note", "opportunity_contact_role_create", "opportunity_contact_role_get_all", "opportunity_delete", "opportunity_get", "opportunity_line_item_create", "opportunity_line_item_get_all", "opportunity_update"}},
	{"campaign_id", "salesforce-lookup?object=Campaign", nil, []string{"campaign_delete", "campaign_get", "campaign_member_create", "campaign_member_delete", "campaign_member_get_all", "campaign_member_update", "campaign_update", "contact_add_to_campaign", "lead_add_to_campaign", "opportunity_create", "opportunity_update", "opportunity_upsert"}},
	{"case_id", "salesforce-lookup?object=Case", nil, []string{"case_add_comment", "case_close", "case_comment_get_all", "case_delete", "case_get", "case_update"}},
	{"task_id", "salesforce-lookup?object=Task", nil, []string{"task_complete", "task_delete", "task_get", "task_update"}},
	{"event_id", "salesforce-lookup?object=Event", nil, []string{"event_delete", "event_delete_series", "event_get", "event_update"}},
	{"attachment_id", "salesforce-lookup?object=Attachment", nil, []string{"attachment_delete", "attachment_download", "attachment_get", "attachment_update"}},
	{"file_id", "salesforce-lookup?object=ContentDocument", nil, []string{"file_delete", "file_download", "file_link_to_record"}},
	{"line_item_id", "salesforce-lookup?object=OpportunityLineItem", nil, []string{"opportunity_line_item_delete", "opportunity_line_item_update"}},
	{"campaign_member_id", "salesforce-lookup?object=CampaignMember", nil, []string{"campaign_member_delete", "campaign_member_update"}},
	{"parent_id", "salesforce-lookup?object=Account", nil, []string{"account_create", "account_update", "account_upsert"}},
	{"parent_id", "salesforce-lookup?object=Campaign", nil, []string{"campaign_create", "campaign_update"}},
	{"parent_id", "salesforce-lookup?object=Case", nil, []string{"case_create", "case_update", "case_upsert", "email_message_create", "email_message_get_all"}},
	{"who_id", "salesforce-lookup?object=Contact,Lead", nil, []string{"event_create", "event_get_all", "event_update", "task_create", "task_get_all", "task_update", "task_upsert"}},
	{"what_id", "salesforce-lookup?object=Account,Opportunity,Case,Campaign", nil, []string{"event_create", "event_get_all", "event_update", "task_create", "task_get_all", "task_update", "task_upsert"}},
	{"related_record_id", "salesforce-lookup?object=Account,Contact,Lead,Opportunity,Case", nil, []string{"email_send"}},
	{"recipient_id", "salesforce-lookup?object=Contact,Lead,User", nil, []string{"email_send"}},
	{"related_to_id", "salesforce-lookup?object=Account,Opportunity,Case,Contact", nil, []string{"email_message_get_all"}},
	{"profile_id", "salesforce-lookup?object=Profile", nil, []string{"user_create", "user_update"}},
	{"user_role_id", "salesforce-lookup?object=UserRole", nil, []string{"user_create", "user_update"}},
	{"pricebook_id", "salesforce-lookup?object=Pricebook2", nil, []string{"opportunity_create", "opportunity_update", "opportunity_upsert"}},
	{"product_id", "salesforce-lookup?object=Product2", nil, []string{"opportunity_line_item_create"}},
	// pricebook_entry_id is NOT here — see the commerce section's
	// /salesforce-price-book-entries group for why the generic lookup cannot
	// label a price book entry.
	{"org_wide_email_address_id", "salesforce-lookup?object=OrgWideEmailAddress", nil, []string{"email_send"}},
	{"email_template_id", "salesforce-lookup?object=EmailTemplate", nil, []string{"email_send"}},
	{"parent_id", "salesforce-lookup", []string{"object"}, []string{"attachment_create", "note_create"}},
	{"related_to_id", "salesforce-lookup", []string{"object"}, []string{"email_message_create"}},
	{"linked_entity_id", "salesforce-lookup", []string{"object"}, []string{"file_link_to_record"}},
	{"link_to_object_id", "salesforce-lookup", []string{"link_to_object"}, []string{"file_upload"}},
	{"context_id", "salesforce-lookup", []string{"object"}, []string{"quick_action_run"}},
	{"master_record_id", "salesforce-lookup", []string{"object"}, []string{"record_merge"}},
	{"record_id", "salesforce-lookup", []string{"object"}, []string{"approval_submit", "file_get_all_for_record", "note_get_all_for_record", "record_get_related", "task_get_all_for_record"}},
	{"record_id", "salesforce-lookup", []string{"custom_object"}, []string{"custom_object_delete", "custom_object_get", "custom_object_update"}},
	// Inputs that can only ever be a User, never a queue.
	{"user_id", "salesforce-users", nil, []string{"user_deactivate", "user_get", "user_update"}},
	{"manager_id", "salesforce-users", nil, []string{"user_create", "user_update"}},
	{"pending_for_user_id", "salesforce-users", nil, []string{"approval_get_all"}},
	{"submitter_id", "salesforce-users", nil, []string{"approval_submit"}},
	{"next_approver_id", "salesforce-users", nil, []string{"approval_approve", "approval_submit"}},
	// owner=true on the plain user picker for the same reason /salesforce-owners
	// applies it: these inputs all become an OwnerId, and a Chatter Free user is
	// refused as one. The inputs above that merely NAME a user (user_id,
	// manager_id, the approvers) keep the unfiltered list.
	{"owner_id", "salesforce-users?owner=true", nil, []string{"account_add_note", "attachment_create", "attachment_update", "contact_add_note", "event_create", "event_get_all", "event_update", "file_upload", "lead_add_note", "lead_convert", "opportunity_add_note"}},
	{"owner_id", "salesforce-owners?object=Account", nil, []string{"account_create", "account_update", "account_upsert"}},
	{"owner_id", "salesforce-owners?object=Campaign", nil, []string{"campaign_create", "campaign_update"}},
	{"owner_id", "salesforce-owners?object=Case", nil, []string{"case_create", "case_update", "case_upsert"}},
	{"owner_id", "salesforce-owners?object=Contact", nil, []string{"contact_create", "contact_update", "contact_upsert"}},
	{"owner_id", "salesforce-owners?object=Lead", nil, []string{"lead_create", "lead_update", "lead_upsert"}},
	{"owner_id", "salesforce-owners?object=Opportunity", nil, []string{"opportunity_create", "opportunity_update", "opportunity_upsert"}},
	{"owner_id", "salesforce-owners?object=Task", nil, []string{"task_create", "task_get_all", "task_update", "task_upsert"}},
	{"owner_id", "salesforce-owners", []string{"custom_object"}, []string{"custom_object_create", "custom_object_update", "custom_object_upsert"}},
	// Member statuses are per campaign, so the picker depends on the chosen campaign.
	{"campaign_member_status", "salesforce-campaign-member-status", []string{"campaign_id"}, []string{"campaign_member_create", "campaign_member_get_all", "campaign_member_update", "contact_add_to_campaign", "lead_add_to_campaign"}},
	{"list_view_id", "salesforce-list-views", []string{"object"}, []string{"list_view_run"}},
	{"report_id", "salesforce-reports", nil, []string{"report_run"}},
	{"folder", "salesforce-reports?folders=true", nil, []string{"report_get_all"}},

	// ═══════════════════════════════════════════════════════════════════════════
	// COMMERCE (v2) — products, price books, quotes, orders, contracts, assets
	// ═══════════════════════════════════════════════════════════════════════════
	//
	// 37 actions, and the pickers matter MORE here than anywhere else in the node:
	// a commerce flow is a chain of ids nobody has. Adding a product to a quote
	// needs a quote id, a product id and a price book ENTRY id, and the operator
	// building the flow has a product name and a price list, which is three
	// lookups away from what the API wants.
	//
	// Everything below reuses the v1 proxies except two inputs no existing proxy
	// can serve honestly — the price book ENTRY (see the group at the end) and two
	// of the four Contract Status inputs, where the org's own picklist contains
	// values that defeat the action (see the picklist group below).
	//
	// Two mappings were checked against the LIVE org rather than assumed, and both
	// came out against the obvious answer:
	//
	//   - owner_id is NOT one picker. Quote.OwnerId and Order.OwnerId describe
	//     referenceTo ["Group","User"] — a queue can own them, so they get
	//     /salesforce-owners. Contract.OwnerId and Asset.OwnerId describe
	//     referenceTo ["User"] — a queue CANNOT, and offering one would be a row
	//     that always fails, so they get /salesforce-users?owner=true. Copying
	//     either answer across all four would have been wrong for two of them.
	//   - product_upsert's match key is NOT an external-id field picker. On a stock
	//     Product2 the only externalId/idLookup fields are Id and Name (verified
	//     live), so /salesforce-external-id-fields would offer exactly two rows and
	//     hide ProductCode and StockKeepingUnit — the keys a spreadsheet or ERP
	//     sync actually has, and which the action supports through its own
	//     match-then-write fallback. It gets the filterable-fields picker instead,
	//     the same one record_find#match_field uses.

	// Record references. Every commerce object labels usefully through the generic
	// lookup: Product2 / Pricebook2 / Quote / Asset flag a real Name, and Order and
	// Contract flag an auto-number that salesforceLookupSecondaryField now widens
	// with the customer's name.
	{"product_id", "salesforce-lookup?object=Product2", nil, []string{"asset_create", "asset_get_all", "asset_update", "order_item_create", "price_book_entry_create", "price_book_entry_get_all", "product_delete", "product_get", "product_update", "quote_line_item_create"}},
	{"pricebook_id", "salesforce-lookup?object=Pricebook2", nil, []string{"contract_create", "contract_update", "order_create", "order_update", "price_book_entry_create", "price_book_entry_get_all", "quote_create", "quote_update"}},
	{"quote_id", "salesforce-lookup?object=Quote", nil, []string{"quote_delete", "quote_get", "quote_line_item_create", "quote_line_item_get_all", "quote_sync_to_opportunity", "quote_update"}},
	{"order_id", "salesforce-lookup?object=Order", nil, []string{"order_activate", "order_delete", "order_get", "order_item_create", "order_item_get_all", "order_update"}},
	{"contract_id", "salesforce-lookup?object=Contract", nil, []string{"contract_activate", "contract_delete", "contract_get", "contract_update", "order_create", "order_update", "quote_create", "quote_update"}},
	{"asset_id", "salesforce-lookup?object=Asset", nil, []string{"asset_delete", "asset_get", "asset_update"}},
	// An asset's parent is another asset — a bundle's components hang off it.
	{"parent_id", "salesforce-lookup?object=Asset", nil, []string{"asset_create", "asset_update"}},
	{"opportunity_line_item_id", "salesforce-lookup?object=OpportunityLineItem", nil, []string{"quote_line_item_create"}},
	// quote_update is deliberately absent: Update Quote has no account input at all
	// (Quote's account comes from QuoteAccountId, which only Create Quote exposes),
	// and a marker on an input the action does not declare renders nothing.
	{"account_id", "salesforce-lookup?object=Account", nil, []string{"asset_create", "asset_get_all", "asset_update", "contract_create", "contract_get_all", "contract_update", "order_create", "order_update", "quote_create"}},
	{"contact_id", "salesforce-lookup?object=Contact", nil, []string{"asset_create", "asset_get_all", "asset_update", "quote_create", "quote_update"}},
	{"bill_to_contact_id", "salesforce-lookup?object=Contact", nil, []string{"order_create", "order_update"}},
	{"ship_to_contact_id", "salesforce-lookup?object=Contact", nil, []string{"order_create", "order_update"}},
	// Contract.CustomerSignedId is a CONTACT (the customer's signatory) while
	// CompanySignedId is a USER (yours) — describe confirms referenceTo Contact and
	// User respectively, so they are two different pickers on adjacent inputs.
	{"customer_signed_id", "salesforce-lookup?object=Contact", nil, []string{"contract_create", "contract_update"}},
	{"asset_provided_by_id", "salesforce-lookup?object=Account", nil, []string{"asset_create", "asset_update"}},
	{"asset_serviced_by_id", "salesforce-lookup?object=Account", nil, []string{"asset_create", "asset_update"}},
	{"opportunity_id", "salesforce-lookup?object=Opportunity", nil, []string{"quote_create", "quote_sync_to_opportunity", "quote_update"}},

	// Owners. Split by what the object's OwnerId can actually hold — see the note
	// at the top of this section.
	{"owner_id", "salesforce-owners?object=Quote", nil, []string{"quote_create", "quote_update"}},
	{"owner_id", "salesforce-owners?object=Order", nil, []string{"order_create", "order_update"}},
	{"owner_id", "salesforce-users?owner=true", nil, []string{"asset_create", "asset_update", "contract_create", "contract_get_all", "contract_update"}},
	// The unfiltered user list, like manager_id: this input NAMES a user rather
	// than making them an owner, so a Chatter Free signatory is legitimate.
	{"company_signed_id", "salesforce-users", nil, []string{"contract_create", "contract_update"}},

	// Picklists. Every one of these is a field whose values the org sets up and the
	// action passes straight through, which is exactly what /salesforce-picklist
	// is for. Verified live: Product2.Family and QuantityUnitOfMeasure are real
	// picklists (the placeholders warn that Salesforce stores anything typed
	// without complaining, which is the harm a picker removes), Contract.Status is
	// restricted to the org's three, Asset.Status to five, and
	// Contract.OwnerExpirationNotice to a day count.
	{"family", "salesforce-picklist?object=Product2&field=Family", nil, []string{"product_create", "product_get_all", "product_update", "product_upsert"}},
	{"quantity_unit_of_measure", "salesforce-picklist?object=Product2&field=QuantityUnitOfMeasure", nil, []string{"product_create", "product_update", "product_upsert"}},
	// Order.Type ships with NO active values in a stock org, and the picker then
	// says so and lets the operator type — which is the honest answer, because the
	// field is unrestricted. Orgs that do configure order types get their own list.
	{"order_type", "salesforce-picklist?object=Order&field=Type", nil, []string{"order_create", "order_update"}},
	// Contract.Status is the one commerce picklist that is NOT one list for all
	// four actions, and the split is not cosmetic — see salesforce_options.go § 14.
	// Changing a contract's status and filtering a report by it can legitimately
	// name any of the org's statuses, so those two keep the picklist. The other two
	// accept exactly one StatusCode category and the other rows are traps:
	// activating with "Draft" is a 204 that leaves the contract a draft while the
	// action reports it activated, and creating with anything but the draft status
	// is 400 FAILED_ACTIVATION every time. Both verified live.
	{"contract_status", "salesforce-picklist?object=Contract&field=Status", nil, []string{"contract_get_all", "contract_update"}},
	{"contract_status", "salesforce-contract-statuses?status_code=Activated", nil, []string{"contract_activate"}},
	{"contract_status", "salesforce-contract-statuses?status_code=Draft", nil, []string{"contract_create"}},
	{"owner_expiration_notice", "salesforce-picklist?object=Contract&field=OwnerExpirationNotice", nil, []string{"contract_create", "contract_update"}},
	{"asset_status", "salesforce-picklist?object=Asset&field=Status", nil, []string{"asset_create", "asset_get_all", "asset_update"}},

	// Record types. Both objects support them; neither has any configured in a
	// stock org, where the picker answers "your org doesn't use record types on
	// Contract — leave this blank" rather than an empty box.
	{"record_type_id", "salesforce-record-types?object=Contract", nil, []string{"contract_create"}},
	{"record_type_id", "salesforce-record-types?object=Asset", nil, []string{"asset_create"}},

	// Field pickers. Same three filters as v1: everything for a SELECT list, only
	// filterable fields for a filter, only sortable ones for a sort.
	{"fields", "salesforce-fields?filter=all&object=Product2", nil, []string{"product_get", "product_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=Pricebook2", nil, []string{"price_book_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=PricebookEntry", nil, []string{"price_book_entry_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=Quote", nil, []string{"quote_get", "quote_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=QuoteLineItem", nil, []string{"quote_line_item_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=Order", nil, []string{"order_get", "order_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=OrderItem", nil, []string{"order_item_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=Contract", nil, []string{"contract_get", "contract_get_all"}},
	{"fields", "salesforce-fields?filter=all&object=Asset", nil, []string{"asset_get", "asset_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Product2", nil, []string{"product_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Pricebook2", nil, []string{"price_book_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=PricebookEntry", nil, []string{"price_book_entry_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Quote", nil, []string{"quote_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Order", nil, []string{"order_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Contract", nil, []string{"contract_get_all"}},
	{"filter_field", "salesforce-fields?filter=filterable&object=Asset", nil, []string{"asset_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Product2", nil, []string{"product_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Pricebook2", nil, []string{"price_book_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=PricebookEntry", nil, []string{"price_book_entry_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Quote", nil, []string{"quote_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=QuoteLineItem", nil, []string{"quote_line_item_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Order", nil, []string{"order_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=OrderItem", nil, []string{"order_item_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Contract", nil, []string{"contract_get_all"}},
	{"order_by", "salesforce-fields?filter=sortable&object=Asset", nil, []string{"asset_get_all"}},
	// Create or Update Product matches on a field, and the action supports any
	// filterable one — Salesforce's own atomic upsert when it is a real External
	// Id, a look-up-then-write when it is not. Same picker as record_find.
	{"match_field", "salesforce-fields?filter=filterable&object=Product2", nil, []string{"product_upsert"}},

	// The one input no existing proxy can serve: a price book ENTRY.
	//
	// /salesforce-lookup?object=PricebookEntry labels rows from the object's own
	// name field, which for PricebookEntry is the PRODUCT's name repeated once per
	// book — verified against the live org, "GenWatt Diesel 1000kW" twice with
	// nothing to tell the two apart. So a dedicated proxy labels book + product +
	// price, and where the action has a parent to go on it scopes the list to the
	// only book Salesforce will accept a line item from (the same sibling-forwarding
	// shape as campaign_member_status). See salesforce_options.go § 13.
	{"pricebook_entry_id", "salesforce-price-book-entries?scope=quote", []string{"quote_id", "product_id"}, []string{"quote_line_item_create"}},
	{"pricebook_entry_id", "salesforce-price-book-entries?scope=order", []string{"order_id", "product_id"}, []string{"order_item_create"}},
	// v1's opportunity line item moves onto the same picker: it had the identical
	// ambiguous-label defect, and Opportunity carries a Pricebook2Id to scope by.
	{"pricebook_entry_id", "salesforce-price-book-entries?scope=opportunity", []string{"opportunity_id", "product_id"}, []string{"opportunity_line_item_create"}},
	// Change Product Price has NO sibling to scope by — its only other inputs are
	// the price and the tick box — so this one lists every book, and keeps the
	// retired entries in because reactivating one is exactly what the action's
	// Ready To Sell box is for.
	{"pricebook_entry_id", "salesforce-price-book-entries?include_inactive=true", nil, []string{"price_book_entry_update"}},
}

// init registers every Salesforce marker into the shared dynamicOptionsMetadata
// map (declared in action.go).
func init() {
	for _, g := range salesforcePickerGroups {
		params := make([]string, 0, len(salesforceAuthParams)+len(g.Extra))
		params = append(params, salesforceAuthParams...)
		params = append(params, g.Extra...)
		for _, action := range g.Actions {
			key := "crm/salesforce/" + action + "#" + g.Input
			if _, dup := dynamicOptionsMetadata[key]; dup {
				// Two groups claiming the same input would silently leave whichever
				// registered last, which is exactly the sort of drift that makes a
				// picker point at the wrong object.
				panic("salesforce options: duplicate marker " + key)
			}
			dynamicOptionsMetadata[key] = api.InputDynamicOptions{
				Endpoint: "/api/v1/action/options/" + g.Endpoint,
				Params:   params,
			}
		}
	}
}
