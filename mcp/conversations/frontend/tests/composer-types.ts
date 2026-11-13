import type { ChatProps, ComposerOptions } from "../dist/react";
const singleLine = { layout: "single-line" } satisfies ComposerOptions;
const compact = { layout: "compact" } satisfies ComposerOptions;
const chat: Pick<ChatProps, "composer" | "locale"> = {composer: singleLine, locale: "fr-FR"};
// @ts-expect-error Unsupported values must fail at the consumer boundary.
const invalid: ChatProps["composer"] = { layout: "tiny" };
void [singleLine, compact, chat, invalid];

import type { ThreadActionOptions } from "../dist/react";
const threadActions = {actions:[{id:"inspect",label:"Inspect",onSelect:(conversation,client)=>{void client.get(conversation.id)}}]} satisfies ThreadActionOptions;
const menuProps:Pick<ChatProps,"threadActions">={threadActions};
void menuProps;

const ghostOptions:Pick<ChatProps,"replySuggestions">={replySuggestions:false};
void ghostOptions;

import type { ConversationHost } from "../dist/react";
const host: ConversationHost = {
  capabilities: { ui: { chatAssistantLauncher: true } },
  ui: { request: settings => { void settings.chatAssistant.launcher; return () => {}; } },
};
const hostProps: Pick<ChatProps, "host"> = {host};
// @ts-expect-error Host presentation policies cannot force a disabled launcher on.
host.ui.request({chatAssistant:{launcher:"show"}});
void hostProps;

import type {CardPresentation,CardComponent} from "../dist/react";
const presentation:CardPresentation={mode:"floating",width:400,height:500,active:true};
const reference:CardComponent={app:"computer",name:"browser-view",resource:{app_install_id:19,resource_type:"computer.browser_session",resource_id:"br_example"},props:{session_id:"br_example"}};
void [presentation,reference];
