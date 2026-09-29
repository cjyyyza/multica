package migrations

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Published by this fork through b39e69639 before the first upstream sync.
// Full filenames are schema_migrations identities: renumbering them would
// execute already-applied DDL again. Freeze this exact history, not a numeric
// range or a naming pattern. Future changes require new, uniquely numbered
// migrations; do not extend this inventory to admit new collisions.
var publishedForkMigrationDigests = map[string]string{
	"500_issue_origin_popo_chat":                    "abf3315a4db8ca5c580d236970f620d0adf8de39f06bfc37c979fffee091e989",
	"500_workspace_p4_depots":                       "bbba2b3cea64f6f2e2c0bd4762f3f2c4130fb2dcfbf258b390eafef177b8ea05",
	"500_yixiezuo_integration":                      "a81a659ac750297e5d6c2f16ea9edc51017c51f4308212515e976f083e52dbb2",
	"501_issue_origin_popo_chat_validate":           "d3e3c925d7961a60d9a080d13b6423445c5fb22a502eb7bd4701f6c1ce861537",
	"501_yixiezuo_card_link_workspace_index":        "73ad56169018a8841bcec5076ab6b85006e1ae9510b48620272a3b2499cf171d",
	"502_popo_outbound_queue":                       "07dd5d49eb12e03db144896639667735ba0ca261af5bc56921223c52e71b489a",
	"502_yixiezuo_card_link_dirty_index":            "14afd20f1343a7fc2bc4544a4089be014c7b07f1f0950584690179356a3f5d7d",
	"503_popo_outbound_queue_workspace_index":       "0c7c2eaea93dbee846cc80bd0a5b02d6c3c1712afbe4171f32887ffbfe3a0cb5",
	"503_yixiezuo_manual_import":                    "04e8e66326091d4d944b9eebd016219e457a12a1fa987600893ba981cba31c77",
	"504_popo_bridge_pairing":                       "f3eb96a0dd273764e6f5a6a308bb6b9ef71bcd66b78ef239d2dedaffe31675ed",
	"504_yixiezuo_import_source_index":              "db7732bb2fa9efea686a47ad4982871124c318730416fdee516e0ed02f91d4a4",
	"505_popo_bridge_pairing_code_hash_index":       "1444674c69367ffba325cbc54f9386c31e58ad0542e143cf444ebf87522c59f4",
	"505_yixiezuo_import_issue_index":               "9661c1e32e03ae6630cd2cb5a6c427fa790fd6bb143c7f9c797152c2b69cb2e9",
	"506_popo_bridge_pairing_workspace_index":       "baf18ae93074a6e2bf736a31c15de5bc9af3090e4cdd8bcb4c1c1818a3386c15",
	"506_yixiezuo_operation_pending_index":          "3bd47415aa44b492573271fa59c7721b36c66c7d0c90e02b47c81028e958ffd8",
	"507_popo_bridge":                               "ca64fc608525ac76309cfb51689236826cf2a65af778e374c084999bd46d8d6b",
	"507_yixiezuo_operation_issue_index":            "9a3773aa6e6ecd8489628bfafc418d1c7d86dbc462b12c10c231e67465cfc5f7",
	"508_popo_bridge_token_hash_index":              "3add765a5aae4e0c63f4b978b6d4e1cf9c2bdaec328f469f06ad327888925b3a",
	"509_popo_bridge_workspace_index":               "7df28822020054c5c20f4efe1f24a76b4536ea20600abc407d2c572b40d1ac21",
	"510_popo_bridge_command":                       "af734347978b975f7311f9af15a94ff39f654067b0d0eb1a3589d449338115a5",
	"511_popo_bridge_command_delivery_id_index":     "f95cdfb0231d0741ea4dbfea331a8969614808f2c8c366996310965e93d1c570",
	"512_popo_bridge_command_lease_index":           "7870b84c3800513765757d7749f1820c70a3da9b00936e417c92086e96a1958e",
	"513_popo_bridge_command_workspace_index":       "c8665d0d4489bb3f69bb7a1e570f47b57aff544e126985e73dfc155047297356",
	"514_popo_inbound_event":                        "32487d8d01885ce5285538bc9f0d90db83045b22597c9f1753a62aebca7421b4",
	"515_popo_inbound_event_unique_index":           "343cb4c1dcead74ebff65849c633fef61d8d12005dd7c6b56d74cf1f0ab6d68b",
	"516_popo_inbound_event_workspace_index":        "8ce5dacf4f354ade3a5c8a7f52cdf03bc41284ca8cde069b9cf4bb45af5d76fd",
	"517_channel_outbound_message_issue_comment":    "5c568d23c2f8520483a87b93e5688a73aa51903a0d75108c6ac61253767afbe8",
	"518_channel_outbound_message_issue_index":      "37c366d94ea1820e2c79826321b6990e42a0ddcff326a0d81814ca32699da3c7",
	"519_channel_outbound_message_comment_index":    "b5569d1f7a9e8167921ced0fbecb889a740a7aa655a8c4e7481efe8878d38853",
	"520_channel_issue_source":                      "254726ea7ee33816152c1487ec598a115ae84f3bd00c4be434234fefd2451720",
	"521_channel_issue_source_issue_index":          "522b0478d4ffaeb7fbdaf36b617a6a26c0fe8aa4c21fabd4859445ae72cf2da4",
	"522_channel_issue_source_workspace_index":      "fd3af1f6e5794675ddf9a00e586636547fcf4b01e3f971118e804236bef87569",
	"523_channel_inbound_write":                     "caae44b68037e0ec08f9fa549e2d0343a34de014a148410b67e94f40b181958d",
	"524_channel_inbound_write_unique":              "fd2ba881dfa1696e991513d33a2371fc7ad2ac3c20ffdda594e3bc9e48459f04",
	"525_channel_inbound_write_workspace_index":     "f4b344e095107d7603bec5c29faa0371e035ff601f15ad98be2468e22122fc45",
	"526_channel_inbound_write_comment_index":       "c9878e7d72dce10de54108e08347b1df138998026c975a23a260c241f1f9fb9c",
	"527_popo_media_staging":                        "7b9e50a11c88ee43c63c9abd88e95d2358fd6c4782823f054249d0a00758302d",
	"528_popo_media_staging_unique_index":           "83111a02989c59ba3d5fb991697cb5bc55f440ebe9a51d717bf991b390cfe90b",
	"529_popo_media_staging_workspace_index":        "560dd273095f2aa8a246ad3e7d75d64177c934013fc84ce655b786c6ede2c20c",
	"530_popo_outbound_media_grant":                 "a77c68a01707c8a4d9d8925dd6e3f3caa621eb08123293493870c721bcfaf4fa",
	"531_popo_outbound_media_grant_command_index":   "e06725a5b6f5263f4a1ad3c1b7af2e83865703f1409d3e90ef3d93c203d54721",
	"532_popo_outbound_media_grant_bridge_index":    "ea7463a6cf79cc2c5c9324e9372a0a5d3261f8a8859b6458de14d4db5da136f6",
	"533_popo_outbound_media_grant_workspace_index": "55598478b67389f7732a01c4a4033d3124274c496bffbbc87bcc6866d38fdaa2",
	"534_popo_registration":                         "191e0c57782bdf958ca7b5fa2f3f5efd8f38cf1333872a5812d9cfd6e0159f97",
	"535_popo_registration_workspace_index":         "5dd8b3bc164c9a9133e46a9978c9ff117d69adff95d96575f3baeebbc69292a4",
	"536_popo_bridge_command_installation_nullable": "4ea79b8cff19518d4ad70fcb1807c4a93c5d16a37dc84bc80f2f646b0e858acf",
	"537_popo_delivery_source_identity":             "d4537086268a22aa12f5c74140ab8c5ab1b0b60cbdbe89c26b1c6db7bf32355d",
	"538_yixiezuo_channel_review":                   "4cf8f4cb41cd5b80a5e8b1a94a968f5a09164c12d20261ace8877f979448390f",
	"539_yixiezuo_operation_request_index":          "a8998dc35f41a3a80215ac401dbb6de94ddd39925438be14c9320e0559937741",
	"540_swarm_review":                              "444439345250dba22d2123ce0fa8160ffd46e946f19a40c75fb092038bc3fa93",
	"541_swarm_review_unique_index":                 "dbe08af3158312acdd565cf9f95c5fbef3d901f2f00f7fe3bf79cbf457e72610",
	"542_issue_swarm_review":                        "ea2eb783c8e6e37c80670012a8f8bb4e7e87b32ade7c6d8ebbce804ca6c8169c",
	"543_issue_swarm_review_unique_index":           "eff5636d8f77bcbc29277a2a276ef5b642e4af1dc30543b73fca33c90ef7451f",
	"544_issue_swarm_review_issue_index":            "0c17505a5a8e4d866113d04f887e6fe51766332eece4434faae39f1f5f5e04bb",
	"545_swarm_review_workspace_index":              "50a74dce658c76e60d913c0a9ec74dd804b3c09155690ef5463f039cd01502cc",
	"546_issue_swarm_review_workspace_index":        "2e8afa84317e142d096de21b99b50bccf5cfb608b8845258ec2b005afa20a395",
}

// These exact upstream versions were already published at the same numbers.
// No new filename may borrow a number occupied by the frozen fork history.
var upstreamVersionsSharingForkNumbers = map[int]string{
	500: "500_task_message_call_id",
	501: "501_runtime_profile_runtime_type",
	502: "502_channel_reply_delivery",
	503: "503_channel_reply_delivery_turn_index",
	504: "504_channel_reply_delivery_installation_index",
	505: "505_channel_reply_delivery_binding_index",
	506: "506_channel_reply_delivery_attempt_depth",
	509: "509_issue_wakeup",
	510: "510_wakeup_id",
	511: "511_wakeup_issue",
	512: "512_wakeup_due",
	513: "513_wakeup_receipt_id",
	514: "514_wakeup_receipt_key",
	515: "515_wakeup_receipt_pending",
	516: "516_wakeup_pending_scope",
	518: "518_wakeup_event_capture",
	519: "519_wakeup_event_issue",
	520: "520_collaboration_wakeup_events",
	521: "521_wakeup_workspace_summary",
	522: "522_wakeup_run_lookup",
	523: "523_wakeup_registration_source",
	524: "524_wakeup_workspace_history",
	525: "525_wakeup_active_runs",
	526: "526_wakeup_terminal_runs",
	527: "527_wakeup_receipt_expiry",
	528: "528_wakeup_receipt_coalescing",
	529: "529_wakeup_pending_event",
	530: "530_wakeup_bounded_capture",
	531: "531_wakeup_actor_filter",
	532: "532_wakeup_actor_capture",
	533: "533_wakeup_close_in_app",
	534: "534_drop_comment_agent_delivery",
	535: "535_github_pr_address_index",
	536: "536_issue_duplicate_of",
	537: "537_issue_duplicate_of_index",
	538: "538_task_supplement",
	539: "539_task_supplement_request_index",
	540: "540_task_supplement_capability_index",
	541: "541_task_supplement_comment_index",
	542: "542_task_supplement_primary_key",
	543: "543_task_supplement_teardown_guard",
	544: "544_task_supplement_application_settlement",
	545: "545_pr_auto_complete",
	546: "546_issue_pr_automation_workspace_index",
}

func verifyPublishedForkMigrations(t *testing.T) {
	t.Helper()
	dir := realMigrationsDir(t)
	for version, want := range publishedForkMigrationDigests {
		contents := make([][]byte, 0, 2)
		for _, direction := range []string{"up", "down"} {
			data, err := os.ReadFile(filepath.Join(dir, version+"."+direction+".sql"))
			if err != nil {
				t.Fatalf("published migration %s: %v", version, err)
			}
			// Compare Git's canonical LF content even on CRLF checkouts.
			contents = append(contents, bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n")))
		}
		got := fmt.Sprintf("%x", sha256.Sum256(bytes.Join(contents, []byte{0})))
		if got != want {
			t.Errorf("published fork migration %s changed; add a forward migration instead", version)
		}
	}
}
