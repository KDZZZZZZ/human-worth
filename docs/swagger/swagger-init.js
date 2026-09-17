window.ui = SwaggerUIBundle({
  url: './openapi.yaml',
  dom_id: '#swagger-ui',
  deepLinking: true,
  filter: true,
  docExpansion: 'list',
  defaultModelsExpandDepth: 0,
  displayOperationId: true,
  showExtensions: true,
  supportedSubmitMethods: [],
  validatorUrl: null,
  queryConfigEnabled: false,
  persistAuthorization: false,
});
