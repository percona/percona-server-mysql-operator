// Code generated by go-swagger; DO NOT EDIT.

package version_service

// This file was generated by the swagger tool.
// Editing this file might prove futile when you re-run the swagger generate command

import (
	"fmt"
	"io"

	"github.com/go-openapi/runtime"
	"github.com/go-openapi/strfmt"

	"github.com/percona/percona-server-mysql-operator/pkg/version/service/client/models"
)

// VersionServiceProductReader is a Reader for the VersionServiceProduct structure.
type VersionServiceProductReader struct {
	formats strfmt.Registry
}

// ReadResponse reads a server response into the received o.
func (o *VersionServiceProductReader) ReadResponse(response runtime.ClientResponse, consumer runtime.Consumer) (interface{}, error) {
	switch response.Code() {
	case 200:
		result := NewVersionServiceProductOK()
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		return result, nil
	default:
		result := NewVersionServiceProductDefault(response.Code())
		if err := result.readResponse(response, consumer, o.formats); err != nil {
			return nil, err
		}
		if response.Code()/100 == 2 {
			return result, nil
		}
		return nil, result
	}
}

// NewVersionServiceProductOK creates a VersionServiceProductOK with default headers values
func NewVersionServiceProductOK() *VersionServiceProductOK {
	return &VersionServiceProductOK{}
}

/*
VersionServiceProductOK describes a response with status code 200, with default header values.

A successful response.
*/
type VersionServiceProductOK struct {
	Payload *models.VersionProductResponse
}

// Error formats the VersionServiceProductOK response as an error string.
func (o *VersionServiceProductOK) Error() string {
	return fmt.Sprintf("[GET /versions/v1/{product}][%d] versionServiceProductOK  %+v", 200, o.Payload)
}

// GetPayload retrieves the payload of the VersionServiceProductOK response.
func (o *VersionServiceProductOK) GetPayload() *models.VersionProductResponse {
	return o.Payload
}

// readResponse reads and decodes the client response into the VersionServiceProductOK structure.
func (o *VersionServiceProductOK) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.VersionProductResponse)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}

// NewVersionServiceProductDefault creates a VersionServiceProductDefault with default headers values
func NewVersionServiceProductDefault(code int) *VersionServiceProductDefault {
	return &VersionServiceProductDefault{
		_statusCode: code,
	}
}

/*
VersionServiceProductDefault describes a response with status code -1, with default header values.

An unexpected error response
*/
type VersionServiceProductDefault struct {
	_statusCode int

	Payload *models.GooglerpcStatus
}

// Code gets the status code for the version service product default response
func (o *VersionServiceProductDefault) Code() int {
	return o._statusCode
}

// Error formats the VersionServiceProductDefault response as an error string.
func (o *VersionServiceProductDefault) Error() string {
	return fmt.Sprintf("[GET /versions/v1/{product}][%d] VersionService_Product default  %+v", o._statusCode, o.Payload)
}

// GetPayload retrieves the payload of the VersionServiceProductDefault response.
func (o *VersionServiceProductDefault) GetPayload() *models.GooglerpcStatus {
	return o.Payload
}

// readResponse reads and decodes the client response into the VersionServiceProductDefault structure.
func (o *VersionServiceProductDefault) readResponse(response runtime.ClientResponse, consumer runtime.Consumer, formats strfmt.Registry) error {

	o.Payload = new(models.GooglerpcStatus)

	// response payload
	if err := consumer.Consume(response.Body(), o.Payload); err != nil && err != io.EOF {
		return err
	}

	return nil
}
