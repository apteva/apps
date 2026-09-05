declare module '@apteva/ui-kit' {
 import type {ComponentType,ReactNode} from 'react';
 export type CardVendor={name:string;logo:ReactNode;color:{light:string;dark:string}};
 export const Card:ComponentType<any>;
 export const CardHeader:ComponentType<any>;
 export const DataList:ComponentType<any>;
}
