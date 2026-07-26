package http

// Dynamic-options markers for the CRM ▸ Salesforce actions.
//
// 429 markers over 140 actions, registered from a table in init() rather than
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
//     actions, "campaign_id" for the two-hop member-status picker.
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
	{"pricebook_entry_id", "salesforce-lookup?object=PricebookEntry", nil, []string{"opportunity_line_item_create"}},
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
