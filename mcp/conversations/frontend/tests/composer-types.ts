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
