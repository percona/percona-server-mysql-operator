## Copyright (C) 2022 Dmitriy Kostiuk
##
## This program is free software: you can redistribute it and/or modify
## it under the terms of the GNU General Public License as published by
## the Free Software Foundation, either version 3 of the License, or
## (at your option) any later version.
##
## This program is distributed in the hope that it will be useful,
## but WITHOUT ANY WARRANTY; without even the implied warranty of
## MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
## GNU General Public License for more details.
##
## You should have received a copy of the GNU General Public License
## along with this program.  If not, see <https://www.gnu.org/licenses/>.

## -*- texinfo -*-
## @deftypefn {} {@var{retval} =} risks3d (@var{input1}, @var{input2})
##
## @seealso{}
## @end deftypefn

## Author: Dmitriy Kostiuk <dmitriykostiuk@Dmitriys-MacBook-Pro.local>
## Created: 2022-01-14

#function retval = risks3d (input1, input2)

#endfunction
clear all;
%close all;
tan_x = -5:0.1:5; 
tan_z = tan_x.*0-5;
tan_y = tanh(tan_x).*5;
tan_y2= tanh(tan_x+1).*5;
hold off;
grid on;
hold on;

%plot3(tan_x, tan_y, tan_z);
%plot3(tan_z, tan_x, tan_y);

%plot3(tan_x, tan_y, tan_x.*0);
%plot3(tan_y, tan_x, tan_x.*0);

[xx zz] = meshgrid(tan_x, tan_x);
[xx1 zz1]= meshgrid(tan_x, tan_x./5);

mesh(zz, xx,tanh(xx), 'FaceAlpha',.7, 'EdgeAlpha', 1);

mesh (tanh(xx1+1).*4.7, xx1, 0-zz1, 'FaceAlpha',.3, 'EdgeAlpha', 1);




%plot3(tan_x-tanh(tan_x).*0.7, tan_x-tanh_x,tan_x./5);

%plot3(tan_x, tan_y.*0.8, tan_x.*0+0.3);

